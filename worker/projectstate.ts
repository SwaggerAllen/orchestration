/**
 * ProjectState — the pipeline's record of its own writes, one Durable
 * Object per project.
 *
 * The tracker cannot answer "who moved this ticket". On a solo workspace
 * the harness holds the author's Linear key, so every pipeline write
 * arrives wearing the author's identity, and a role resolved from that
 * identity says "control plane" for the human's moves too — which is the
 * one role the revert rules trust. Every §9 invariant was off, silently,
 * for as long as the two shared an id. The alternative fix is a Linear
 * seat per role per month, to encode something the pipeline already
 * knows about itself.
 *
 * So the pipeline writes down what it is about to do, before doing it,
 * and the sweep compares that against where the tracker says the ticket
 * is. Agreement means the pipeline made the last move; disagreement
 * means somebody else did.
 *
 * SQLite rather than the key-value storage API, because this is the
 * first tenant of the store DESIGN §13 has been pointing at — and
 * because the next things to move here (run correlation, and the
 * single-agent mutex the Durable Object *is* by construction) want
 * queries rather than a bag of keys.
 *
 * One row per ticket. This is not an event log: the only question asked
 * of it is "where did the pipeline last leave this ticket, and as whom",
 * and keeping history would mean deciding when to prune it.
 */

export interface ProjectStateEnv {
  /** Shared secret the harness presents. See the auth note in fetch(). */
  STATE_TOKEN: string;
}

interface Move {
  from: string;
  to: string;
  role: string;
}

export class ProjectState {
  private sql: SqlStorage;
  private env: ProjectStateEnv;

  constructor(state: DurableObjectState, env: ProjectStateEnv) {
    this.sql = state.storage.sql;
    this.env = env;
    // Idempotent, and cheap enough to run per instantiation rather than
    // carrying a migration story for one table.
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS moves (
        ticket_id TEXT PRIMARY KEY,
        from_state TEXT NOT NULL,
        to_state TEXT NOT NULL,
        role TEXT NOT NULL,
        at INTEGER NOT NULL
      )
    `);
    // Dispatch reservations, one row per agent kind.
    //
    // The sweep's singularity guard reads a run marker the harness posts
    // at *claim* — inside the dispatched job, after runner boot,
    // checkout, toolchain and dependency install. Measured on catapult:
    // about ninety seconds between the dispatch and the marker proving
    // it happened. Any sweep landing in that window sees an idle agent
    // and dispatches again, and two boundary agents ran one ticket to
    // completion that way: two scans, twenty-two minutes of model spend,
    // four tickets for two findings.
    //
    // A reservation is written by the thing that *decides* — the sweep,
    // before it dispatches — so the record exists before the next sweep
    // can read it. That closes the window rather than narrowing it.
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS reservations (
        kind TEXT PRIMARY KEY,
        ticket_id TEXT NOT NULL,
        at INTEGER NOT NULL
      )
    `);
  }

  async fetch(request: Request): Promise<Response> {
    // Bearer auth against a shared secret. The harness runs in Actions
    // and cannot present anything better, and the blast radius is
    // bounded by what this object does: it holds no credentials and can
    // move nothing. A forged write makes the sweep misjudge one ticket's
    // provenance — bad, and not the same order of bad as a token that
    // can start workflows.
    const auth = request.headers.get("authorization");
    if (!this.env.STATE_TOKEN || auth !== `Bearer ${this.env.STATE_TOKEN}`) {
      return new Response("unauthorized\n", { status: 401 });
    }
    const url = new URL(request.url);

    if (request.method === "GET" && url.pathname === "/all") {
      const out: Record<string, Move> = {};
      for (const row of this.sql.exec("SELECT ticket_id, from_state, to_state, role FROM moves")) {
        out[row.ticket_id as string] = {
          from: row.from_state as string,
          to: row.to_state as string,
          role: row.role as string,
        };
      }
      return Response.json(out);
    }

    // Reserve is compare-and-set, and it is the whole point: two sweeps
    // racing both call it and exactly one is told it won. Durable
    // Objects serialize requests to one instance, so this needs no
    // transaction beyond that guarantee — which is the property DESIGN
    // §13 named when it said one object per project *is* the mutex.
    if (request.method === "POST" && url.pathname === "/reserve") {
      const body = (await request.json()) as { kind: string; ticket: string; ttlMs: number };
      if (!body?.kind || !body?.ticket) {
        return new Response("kind and ticket are required\n", { status: 400 });
      }
      // A reservation expires. A dispatched job can die before it ever
      // claims — a runner lost, a workflow file that will not parse —
      // and a lock nobody can release is an agent kind that never runs
      // again. The TTL is the caller's, because only the caller knows
      // how long boot-to-claim takes for that project.
      const ttl = typeof body.ttlMs === "number" && body.ttlMs > 0 ? body.ttlMs : 300_000;
      const now = Date.now();
      const held = [...this.sql.exec(
        "SELECT ticket_id, at FROM reservations WHERE kind = ? AND at > ?",
        body.kind, now - ttl,
      )];
      if (held.length > 0 && held[0].ticket_id !== body.ticket) {
        return Response.json(
          { granted: false, heldBy: held[0].ticket_id, since: held[0].at },
          { status: 200 },
        );
      }
      this.sql.exec(
        `INSERT INTO reservations (kind, ticket_id, at) VALUES (?, ?, ?)
         ON CONFLICT(kind) DO UPDATE SET ticket_id = excluded.ticket_id, at = excluded.at`,
        body.kind, body.ticket, now,
      );
      return Response.json({ granted: true }, { status: 200 });
    }

    // Released when a run ends, so the next dispatch does not wait out
    // the TTL. Best-effort: the TTL is what makes correctness not depend
    // on this arriving.
    if (request.method === "POST" && url.pathname === "/release") {
      const body = (await request.json()) as { kind: string; ticket: string };
      if (!body?.kind) {
        return new Response("kind is required\n", { status: 400 });
      }
      this.sql.exec("DELETE FROM reservations WHERE kind = ? AND ticket_id = ?", body.kind, body.ticket ?? "");
      return new Response("released\n", { status: 200 });
    }

    if (request.method === "POST" && url.pathname === "/record") {
      const body = (await request.json()) as { ticket: string } & Move;
      if (!body?.ticket || !body?.to || !body?.role) {
        // Rejected rather than stored partially: the caller treats a
        // failure as "do not make the move", which is the safe outcome,
        // while a half-written record is one the sweep would act on.
        return new Response("ticket, to and role are required\n", { status: 400 });
      }
      this.sql.exec(
        `INSERT INTO moves (ticket_id, from_state, to_state, role, at)
         VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(ticket_id) DO UPDATE SET
           from_state = excluded.from_state,
           to_state = excluded.to_state,
           role = excluded.role,
           at = excluded.at`,
        body.ticket,
        body.from ?? "",
        body.to,
        body.role,
        Date.now(),
      );
      return new Response("recorded\n", { status: 200 });
    }

    return new Response("not found\n", { status: 404 });
  }
}
