/**
 * ProjectStats — the pipeline's measurement of itself, one Durable
 * Object per project (DESIGN §13).
 *
 * A second class rather than more tables on ProjectState, and the
 * reason is the serialization guarantee that makes ProjectState useful.
 * Durable Objects serialize requests to one instance, and ProjectState
 * is on the critical path: every sweep reads /all, every dispatch calls
 * /reserve. A dashboard aggregation queued ahead of a reservation would
 * widen exactly the dispatch window the reservation exists to close.
 * Same Worker, same deploy, same auth idiom — its own lane.
 *
 * The rows here are derived, never authored. The collector recomputes
 * them from the tracker and the host, so a backfill and a nightly pass
 * are the same code with a different window, and a defect in the
 * aggregation is repaired by running it again rather than by migrating
 * what it already wrote.
 *
 * THE ASYMMETRY THIS STORE ENFORCES. Linear's archive is a visibility
 * flag rather than a deletion — an archived ticket still returns its
 * full state history — so the tracker half is a full recompute and
 * idempotent upsert, safe to rebuild forever. GitHub's run data ages
 * out, so once a run passes the host's horizon the row here is the only
 * copy. There is therefore no route that deletes a run. Not "the
 * collector is careful not to": the capability is absent, because the
 * failure is silent, arrives months later, and destroys data nothing can
 * restore.
 */

export interface ProjectStatsEnv {
  /** Shared secret the harness and the dashboard present. */
  STATE_TOKEN: string;
}

/**
 * The subset of Cloudflare's SqlStorage this object uses. Declared
 * rather than imported so the tests can supply a real SQLite behind the
 * same shape — faking the SQL itself would mean the aggregation, which
 * is the entire product here, was never executed by anything.
 */
export interface SqlLike {
  exec(query: string, ...bindings: unknown[]): Iterable<Record<string, unknown>>;
}

/** Buckets the read side will group by. Allowlisted, never interpolated raw. */
const BUCKETS: Record<string, string> = {
  day: "%Y-%m-%d",
  week: "%Y-W%W",
  month: "%Y-%m",
};

/**
 * States an agent holds a ticket in. Named here rather than derived
 * from the tracker's category, because "started" also covers Blocked
 * and Boundary review — states where nothing is running and no minute
 * is being spent. The collective agent-time figure is only meaningful
 * against this list.
 */
const AGENT_STATES = ["designing", "design_review", "in_progress", "reconciling", "checks"];

export class ProjectStats {
  private sql: SqlLike;
  private env: ProjectStatsEnv;

  constructor(state: { storage: { sql: SqlLike } }, env: ProjectStatsEnv) {
    this.sql = state.storage.sql;
    this.env = env;
    this.migrate();
  }

  private migrate(): void {
    // Idempotent CREATE IF NOT EXISTS per instantiation, matching
    // ProjectState: cheaper than carrying a migration story, and these
    // rows are derived, so the recovery for a schema change is to drop
    // and re-collect the tracker half rather than to migrate it.
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS milestone (
        name TEXT PRIMARY KEY,
        kind TEXT NOT NULL,
        window_start INTEGER NOT NULL,
        window_end INTEGER,
        boundary_ticket TEXT,
        boundary_open_start INTEGER,
        boundary_open_end INTEGER
      )
    `);
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS ticket (
        key TEXT PRIMARY KEY,
        title TEXT NOT NULL DEFAULT '',
        created_at INTEGER NOT NULL,
        completed_at INTEGER,
        canceled_at INTEGER,
        priority INTEGER NOT NULL DEFAULT 0,
        is_boundary INTEGER NOT NULL DEFAULT 0,
        archived INTEGER NOT NULL DEFAULT 0
      )
    `);
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS ticket_label (
        ticket_key TEXT NOT NULL,
        label TEXT NOT NULL,
        PRIMARY KEY (ticket_key, label)
      )
    `);
    // A join table rather than a JSON column on ticket: the filters
    // this exists to serve are "has X" and "lacks Y", which are EXISTS
    // and NOT EXISTS against this, and a blob would push both into the
    // caller.
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS interval (
        ticket_key TEXT NOT NULL,
        state TEXT NOT NULL,
        entered_at INTEGER NOT NULL,
        left_at INTEGER,
        PRIMARY KEY (ticket_key, state, entered_at)
      )
    `);
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS run (
        run_id INTEGER PRIMARY KEY,
        repo TEXT NOT NULL,
        workflow TEXT NOT NULL,
        run_name TEXT NOT NULL DEFAULT '',
        kind TEXT NOT NULL DEFAULT '',
        ticket_key TEXT,
        started_at INTEGER NOT NULL,
        duration_ms INTEGER NOT NULL DEFAULT 0,
        billable_ms INTEGER NOT NULL DEFAULT 0,
        job_count INTEGER NOT NULL DEFAULT 0,
        conclusion TEXT NOT NULL DEFAULT '',
        attempt INTEGER NOT NULL DEFAULT 1,
        is_stats_job INTEGER NOT NULL DEFAULT 0
      )
    `);
    this.sql.exec(`
      CREATE TABLE IF NOT EXISTS watermark (
        source TEXT PRIMARY KEY,
        oldest_complete INTEGER,
        newest_seen INTEGER,
        at INTEGER NOT NULL
      )
    `);
    this.sql.exec(`CREATE INDEX IF NOT EXISTS interval_by_ticket ON interval (ticket_key)`);
    this.sql.exec(`CREATE INDEX IF NOT EXISTS run_by_started ON run (started_at)`);
  }

  async fetch(request: Request): Promise<Response> {
    const auth = request.headers.get("authorization");
    if (!this.env.STATE_TOKEN || auth !== `Bearer ${this.env.STATE_TOKEN}`) {
      return new Response("unauthorized\n", { status: 401 });
    }
    const url = new URL(request.url);
    const path = url.pathname;

    if (request.method === "POST" && path === "/tickets") {
      return this.putTickets(await request.json());
    }
    if (request.method === "POST" && path === "/milestones") {
      return this.putMilestones(await request.json());
    }
    if (request.method === "POST" && path === "/runs") {
      return this.putRuns(await request.json());
    }
    if (request.method === "POST" && path === "/watermark") {
      return this.putWatermark(await request.json());
    }
    if (request.method === "GET" && path === "/watermark") {
      return Response.json([...this.sql.exec("SELECT * FROM watermark")]);
    }
    if (request.method === "GET" && path === "/milestones") {
      return Response.json([...this.sql.exec("SELECT * FROM milestone ORDER BY window_start")]);
    }
    if (request.method === "GET" && path === "/states") {
      return this.stateStats(url.searchParams);
    }
    if (request.method === "GET" && path === "/minutes") {
      return this.minutes(url.searchParams);
    }

    // No DELETE, for anything. See the note on the class: run rows
    // outlive the host's own record of them, so the store cannot be
    // asked to drop one.
    return new Response("not found\n", { status: 404 });
  }

  // ---- writes -------------------------------------------------------

  /**
   * Tickets, their labels and their state intervals, as one document.
   *
   * A ticket's intervals are replaced wholesale rather than merged: the
   * tracker's history is authoritative and complete on every read, so
   * merging could only ever preserve a row the tracker no longer claims
   * — which is a row we invented. Same for labels.
   */
  private putTickets(body: any): Response {
    const tickets = Array.isArray(body?.tickets) ? body.tickets : null;
    if (!tickets) {
      return new Response("tickets[] is required\n", { status: 400 });
    }
    for (const t of tickets) {
      if (!t?.key || typeof t.created_at !== "number") {
        return new Response("each ticket needs key and created_at\n", { status: 400 });
      }
    }
    for (const t of tickets) {
      this.sql.exec(
        `INSERT INTO ticket (key, title, created_at, completed_at, canceled_at,
                             priority, is_boundary, archived)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(key) DO UPDATE SET
           title = excluded.title,
           created_at = excluded.created_at,
           completed_at = excluded.completed_at,
           canceled_at = excluded.canceled_at,
           priority = excluded.priority,
           is_boundary = excluded.is_boundary,
           archived = excluded.archived`,
        t.key, t.title ?? "", t.created_at, t.completed_at ?? null, t.canceled_at ?? null,
        t.priority ?? 0, t.is_boundary ? 1 : 0, t.archived ? 1 : 0,
      );
      this.sql.exec("DELETE FROM ticket_label WHERE ticket_key = ?", t.key);
      for (const label of t.labels ?? []) {
        this.sql.exec(
          "INSERT OR IGNORE INTO ticket_label (ticket_key, label) VALUES (?, ?)",
          t.key, label,
        );
      }
      this.sql.exec("DELETE FROM interval WHERE ticket_key = ?", t.key);
      for (const iv of t.intervals ?? []) {
        this.sql.exec(
          `INSERT OR REPLACE INTO interval (ticket_key, state, entered_at, left_at)
           VALUES (?, ?, ?, ?)`,
          t.key, iv.state, iv.entered_at, iv.left_at ?? null,
        );
      }
    }
    return Response.json({ tickets: tickets.length });
  }

  private putMilestones(body: any): Response {
    const milestones = Array.isArray(body?.milestones) ? body.milestones : null;
    if (!milestones) {
      return new Response("milestones[] is required\n", { status: 400 });
    }
    for (const m of milestones) {
      if (!m?.name || typeof m.window_start !== "number") {
        return new Response("each milestone needs name and window_start\n", { status: 400 });
      }
    }
    for (const m of milestones) {
      this.sql.exec(
        `INSERT INTO milestone (name, kind, window_start, window_end, boundary_ticket,
                                boundary_open_start, boundary_open_end)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(name) DO UPDATE SET
           kind = excluded.kind,
           window_start = excluded.window_start,
           window_end = excluded.window_end,
           boundary_ticket = excluded.boundary_ticket,
           boundary_open_start = excluded.boundary_open_start,
           boundary_open_end = excluded.boundary_open_end`,
        m.name, m.kind ?? "feature", m.window_start, m.window_end ?? null,
        m.boundary_ticket ?? null, m.boundary_open_start ?? null, m.boundary_open_end ?? null,
      );
    }
    return Response.json({ milestones: milestones.length });
  }

  /**
   * Runs are insert-only. An existing row is left exactly as it was
   * even when the caller sends a different one.
   *
   * That is not an optimisation and it is not idempotence for its own
   * sake. A run ages out of the host's API; the row here then becomes
   * the only record that it ever ran, and a later collector pass —
   * which by then cannot see the run at all — must not be able to
   * overwrite it with a zero. Insert-only makes re-running the
   * collector safe at any point in the future, which is the property
   * that lets the backfill span nights and the rate limit stop
   * mattering.
   */
  private putRuns(body: any): Response {
    const runs = Array.isArray(body?.runs) ? body.runs : null;
    if (!runs) {
      return new Response("runs[] is required\n", { status: 400 });
    }
    for (const r of runs) {
      if (typeof r?.run_id !== "number" || !r?.repo || typeof r.started_at !== "number") {
        return new Response("each run needs run_id, repo and started_at\n", { status: 400 });
      }
    }
    let inserted = 0;
    for (const r of runs) {
      const before = this.countRuns();
      this.sql.exec(
        `INSERT OR IGNORE INTO run (run_id, repo, workflow, run_name, kind, ticket_key,
                                    started_at, duration_ms, billable_ms, job_count,
                                    conclusion, attempt, is_stats_job)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        r.run_id, r.repo, r.workflow ?? "", r.run_name ?? "", r.kind ?? "",
        r.ticket_key ?? null, r.started_at, r.duration_ms ?? 0, r.billable_ms ?? 0,
        r.job_count ?? 0, r.conclusion ?? "", r.attempt ?? 1, r.is_stats_job ? 1 : 0,
      );
      if (this.countRuns() > before) inserted++;
    }
    return Response.json({ received: runs.length, inserted });
  }

  private countRuns(): number {
    return Number([...this.sql.exec("SELECT COUNT(*) AS c FROM run")][0].c);
  }

  private putWatermark(body: any): Response {
    if (!body?.source) {
      return new Response("source is required\n", { status: 400 });
    }
    this.sql.exec(
      `INSERT INTO watermark (source, oldest_complete, newest_seen, at)
       VALUES (?, ?, ?, ?)
       ON CONFLICT(source) DO UPDATE SET
         oldest_complete = excluded.oldest_complete,
         newest_seen = excluded.newest_seen,
         at = excluded.at`,
      body.source, body.oldest_complete ?? null, body.newest_seen ?? null, Date.now(),
    );
    return new Response("recorded\n", { status: 200 });
  }

  // ---- reads --------------------------------------------------------

  /**
   * Ticket filters shared by every read: a time window, a milestone,
   * and label set membership.
   *
   * Every caller-supplied value is a bound parameter. The only thing
   * interpolated is the timestamp column name, and it is chosen from a
   * two-item allowlist rather than taken from the query string — the
   * question "bugs this milestone produced" wants created_at and "work
   * it completed" wants completed_at, and they are different numbers.
   */
  private ticketFilter(q: URLSearchParams): { sql: string; binds: unknown[] } {
    const on = q.get("on") === "created" ? "created_at" : "completed_at";
    const clauses: string[] = [];
    const binds: unknown[] = [];

    const milestone = q.get("milestone");
    if (milestone) {
      clauses.push(
        `t.${on} > (SELECT window_start FROM milestone WHERE name = ?)
         AND (t.${on} <= (SELECT COALESCE(window_end, 9e18) FROM milestone WHERE name = ?))`,
      );
      binds.push(milestone, milestone);
    }
    const from = q.get("from");
    if (from) {
      clauses.push(`t.${on} >= ?`);
      binds.push(Number(from));
    }
    const to = q.get("to");
    if (to) {
      clauses.push(`t.${on} <= ?`);
      binds.push(Number(to));
    }
    if (q.get("milestoneKind")) {
      clauses.push(
        `EXISTS (SELECT 1 FROM milestone m WHERE m.kind = ?
                 AND t.${on} > m.window_start
                 AND t.${on} <= COALESCE(m.window_end, 9e18))`,
      );
      binds.push(q.get("milestoneKind"));
    }
    if (q.get("boundaryOpen") === "1") {
      clauses.push(
        `EXISTS (SELECT 1 FROM milestone m WHERE m.boundary_open_start IS NOT NULL
                 AND t.${on} >= m.boundary_open_start
                 AND t.${on} <= COALESCE(m.boundary_open_end, 9e18))`,
      );
    }
    for (const label of splitLabels(q.get("hasLabels"))) {
      clauses.push(`EXISTS (SELECT 1 FROM ticket_label l WHERE l.ticket_key = t.key AND l.label = ?)`);
      binds.push(label);
    }
    for (const label of splitLabels(q.get("lacksLabels"))) {
      clauses.push(`NOT EXISTS (SELECT 1 FROM ticket_label l WHERE l.ticket_key = t.key AND l.label = ?)`);
      binds.push(label);
    }
    if (q.get("includeBoundary") !== "1") {
      // The boundary ticket is pipeline machinery whose "time in
      // status" is a human's manual pass, not the pipeline's work. It
      // would dominate every average it appeared in.
      clauses.push("t.is_boundary = 0");
    }
    clauses.push(`t.${on} IS NOT NULL`);
    return { sql: clauses.length ? `WHERE ${clauses.join(" AND ")}` : "", binds };
  }

  /**
   * Time in each state and instances of each state, for the tickets the
   * filter selects, bucketed by the ticket's own timestamp.
   *
   * Cohort rather than flow: an interval counts entirely in the bucket
   * the ticket falls in, not split across the days it spanned. Tickets
   * here are rarely open more than a day, so the two agree closely, and
   * cohort is the reading the trend questions want — "tickets that
   * landed this week spent N hours in design".
   */
  private stateStats(q: URLSearchParams): Response {
    const fmt = BUCKETS[q.get("bucket") ?? ""] ?? null;
    const on = q.get("on") === "created" ? "created_at" : "completed_at";
    const { sql: where, binds } = this.ticketFilter(q);
    const bucket = fmt ? `strftime('${fmt}', t.${on} / 1000, 'unixepoch')` : `'all'`;

    const rows = [...this.sql.exec(
      `SELECT ${bucket} AS bucket,
              i.state AS state,
              COUNT(*) AS instances,
              COUNT(DISTINCT t.key) AS tickets,
              SUM(COALESCE(i.left_at, i.entered_at) - i.entered_at) AS total_ms
         FROM ticket t
         JOIN interval i ON i.ticket_key = t.key
         ${where}
        GROUP BY bucket, state
        ORDER BY bucket, state`,
      ...binds,
    )];

    // The collective agent figure is a separate row rather than a sum
    // the caller makes, because which states count as agent time is a
    // claim about the protocol and belongs next to it.
    const agentPlaceholders = AGENT_STATES.map(() => "?").join(",");
    const agent = [...this.sql.exec(
      `SELECT ${bucket} AS bucket,
              COUNT(DISTINCT t.key) AS tickets,
              SUM(COALESCE(i.left_at, i.entered_at) - i.entered_at) AS total_ms
         FROM ticket t
         JOIN interval i ON i.ticket_key = t.key
         ${where}${where ? " AND" : "WHERE"} i.state IN (${agentPlaceholders})
        GROUP BY bucket
        ORDER BY bucket`,
      ...binds, ...AGENT_STATES,
    )];

    return Response.json({ states: rows, agentTotal: agent, agentStates: AGENT_STATES });
  }

  /**
   * Actions minutes, bucketed by run start.
   *
   * `billable_ms` is the collector's per-job ceiling, not the host's own
   * figure — which measured zero on every run checked. Reporting the raw
   * sum alongside it makes minute-rounding waste readable as the
   * difference rather than as an estimate, and it is a large fraction of
   * the bill for short frequent jobs like the sweep, whose remedy is a
   * wider debounce rather than a different runner.
   */
  private minutes(q: URLSearchParams): Response {
    const fmt = BUCKETS[q.get("bucket") ?? ""] ?? null;
    const bucket = fmt ? `strftime('${fmt}', started_at / 1000, 'unixepoch')` : `'all'`;
    const clauses: string[] = [];
    const binds: unknown[] = [];
    if (q.get("from")) {
      clauses.push("started_at >= ?");
      binds.push(Number(q.get("from")));
    }
    if (q.get("to")) {
      clauses.push("started_at <= ?");
      binds.push(Number(q.get("to")));
    }
    if (q.get("repo")) {
      clauses.push("repo = ?");
      binds.push(q.get("repo"));
    }
    if (q.get("statsJob") !== "1") {
      clauses.push("is_stats_job = 0");
    }
    const where = clauses.length ? `WHERE ${clauses.join(" AND ")}` : "";

    const by = q.get("by") === "kind" ? "kind" : "workflow";
    const rows = [...this.sql.exec(
      `SELECT ${bucket} AS bucket,
              ${by} AS series,
              COUNT(*) AS runs,
              SUM(billable_ms) AS billable_ms,
              SUM(duration_ms) AS duration_ms,
              SUM(billable_ms - duration_ms) AS rounding_ms
         FROM run
         ${where}
        GROUP BY bucket, series
        ORDER BY bucket, series`,
      ...binds,
    )];
    return Response.json({ minutes: rows, groupedBy: by });
  }
}

/** Comma-separated label lists, empties dropped. */
function splitLabels(raw: string | null): string[] {
  if (!raw) return [];
  return raw.split(",").map((s) => s.trim()).filter((s) => s.length > 0);
}
