/**
 * Run with: node --test --experimental-strip-types projectstats.test.ts
 * (or let the suite glob pick it up alongside index.test.ts).
 *
 * These run against real SQLite via node:sqlite, deliberately. The
 * product here is the aggregation, and a fake SqlStorage would have to
 * reimplement GROUP BY and strftime to answer at all — at which point
 * the fake and the code would agree with each other while both
 * disagreed with the database.
 */
import { test } from "node:test";
import assert from "node:assert";

import { ProjectStats } from "./projectstats.ts";
import { memorySql } from "./sqlite.ts";

const TOKEN = "stats-token";

function store(): ProjectStats {
  return new ProjectStats({ storage: { sql: memorySql() } }, { STATE_TOKEN: TOKEN });
}

function req(method: string, path: string, body?: unknown): Request {
  return new Request(`https://stats${path}`, {
    method,
    headers: { authorization: `Bearer ${TOKEN}` },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

const DAY = 86_400_000;
const T0 = Date.UTC(2026, 7, 20); // 2026-08-20

/** A ticket that spent `designMs` in Designing and closed on `day`. */
function ticket(key: string, day: number, designMs: number, extra: Record<string, unknown> = {}) {
  const closed = T0 + day * DAY;
  return {
    key,
    title: key,
    created_at: closed - designMs - 1000,
    completed_at: closed,
    labels: [],
    intervals: [
      { state: "designing", entered_at: closed - designMs, left_at: closed },
    ],
    ...extra,
  };
}

test("an unauthenticated request is refused before it can read anything", async () => {
  const s = store();
  const res = await s.fetch(new Request("https://stats/milestones"));
  assert.equal(res.status, 401);
});

test("tickets round-trip with their labels and intervals", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [ticket("ORC-1", 1, 3600_000, { labels: ["bug", "system:engine"] })],
  }));
  const res = await s.fetch(req("GET", "/states"));
  const body = await res.json() as any;
  assert.equal(body.states.length, 1);
  assert.equal(body.states[0].state, "designing");
  assert.equal(body.states[0].total_ms, 3600_000);
  assert.equal(body.states[0].instances, 1);
});

test("re-collecting an unchanged ticket does not double its interval", async () => {
  const s = store();
  const t = ticket("ORC-1", 1, 3600_000);
  await s.fetch(req("POST", "/tickets", { tickets: [t] }));
  await s.fetch(req("POST", "/tickets", { tickets: [t] }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(body.states[0].instances, 1, "a second collection duplicated the interval");
  assert.equal(body.states[0].total_ms, 3600_000);
});

// The interval and label wipes are only load-bearing when the tracker's
// answer CHANGES. Re-sending an identical ticket replaces each row via
// its own primary key, so the first version of this test passed with
// both wipes deleted — it never asked the question the wipes exist to
// answer. The tracker's history is authoritative and complete on every
// read, so a row it no longer claims is a row we invented.
test("re-collecting a changed ticket drops the intervals the tracker no longer claims", async () => {
  const s = store();
  const closed = T0 + DAY;
  await s.fetch(req("POST", "/tickets", {
    tickets: [{
      key: "ORC-1", created_at: closed - 10_000, completed_at: closed,
      labels: ["bug", "needs-review"],
      intervals: [{ state: "designing", entered_at: closed - 9_000, left_at: closed }],
    }],
  }));
  // The tracker now reports a different history and a different label
  // set for the same ticket — a corrected timestamp, a state that is
  // gone, a label removed.
  await s.fetch(req("POST", "/tickets", {
    tickets: [{
      key: "ORC-1", created_at: closed - 10_000, completed_at: closed,
      labels: ["bug"],
      intervals: [{ state: "in_progress", entered_at: closed - 4_000, left_at: closed }],
    }],
  }));

  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.deepEqual(body.states.map((r: any) => r.state), ["in_progress"],
    "a state the tracker no longer reports survived the recompute");
  assert.equal(body.states[0].total_ms, 4_000);

  const stale = await (await s.fetch(req("GET", "/states?hasLabels=needs-review"))).json() as any;
  assert.equal(stale.states.length, 0, "a label the tracker no longer reports survived the recompute");
});

// The whole reason this store exists as a recompute rather than a
// collector: running it again must be safe, and the tracker half is
// where "again" means "replace".
test("rework instances are counted per entry, not per ticket", async () => {
  const s = store();
  const closed = T0 + DAY;
  await s.fetch(req("POST", "/tickets", {
    tickets: [{
      key: "ORC-2",
      created_at: closed - 10 * 3600_000,
      completed_at: closed,
      intervals: [
        { state: "ready_for_rework", entered_at: closed - 9 * 3600_000, left_at: closed - 8 * 3600_000 },
        { state: "ready_for_rework", entered_at: closed - 5 * 3600_000, left_at: closed - 4 * 3600_000 },
        { state: "ready_for_rework", entered_at: closed - 3 * 3600_000, left_at: closed - 2 * 3600_000 },
      ],
    }],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  const rework = body.states.find((r: any) => r.state === "ready_for_rework");
  assert.equal(rework.instances, 3, "three rework entries did not count as three");
  assert.equal(rework.tickets, 1);
});

// The label sets here are deliberately lopsided. An earlier version
// used one ticket with `harness` and one without, so "lacks harness"
// and "has harness" both selected exactly one ticket — and swapping
// NOT EXISTS for EXISTS passed the test. A count assertion can only
// separate the two readings when the two readings have different
// counts.
test("hasLabels and lacksLabels select and exclude", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [
      ticket("ORC-1", 1, 1000, { labels: ["bug", "system:engine"] }),
      ticket("ORC-2", 1, 1000, { labels: ["bug", "harness"] }),
      ticket("ORC-3", 1, 1000, { labels: ["bug", "harness"] }),
      ticket("ORC-4", 1, 1000, { labels: ["system:engine"] }),
    ],
  }));
  const has = await (await s.fetch(req("GET", "/states?hasLabels=bug"))).json() as any;
  assert.equal(has.states[0].tickets, 3);

  const both = await (await s.fetch(req("GET", "/states?hasLabels=bug,system:engine"))).json() as any;
  assert.equal(both.states[0].tickets, 1, "two hasLabels did not intersect");

  // Correct: {ORC-1}. Inverted: {ORC-2, ORC-3}. One versus two.
  const lacks = await (await s.fetch(req("GET", "/states?hasLabels=bug&lacksLabels=harness"))).json() as any;
  assert.equal(lacks.states[0].tickets, 1, "lacksLabels did not exclude");
});

// Archived tickets are the oldest ones, so dropping them renders as a
// downward trend with nothing in the output looking wrong. The
// collector asks the tracker for them; the store must not then filter
// them back out.
test("an archived ticket is counted like any other", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [
      ticket("ORC-1", 1, 1000, { archived: true }),
      ticket("ORC-2", 1, 1000),
    ],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(body.states[0].tickets, 2, "the archived ticket was dropped");
});

// Three answers, not two. Boundary tickets are shaped nothing like
// ordinary ones — their time in status is a manual pass — so they must
// be excludable, includable, and askable about on their own.
test("boundary tickets can be excluded, included, or asked about alone", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [
      ticket("ORC-1", 1, 1000),
      ticket("ORC-9", 1, 999_000, { is_boundary: true }),
    ],
  }));
  const off = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(off.states[0].tickets, 1);
  assert.equal(off.states[0].total_ms, 1000, "the boundary ticket dominated the total");

  const both = await (await s.fetch(req("GET", "/states?boundary=include"))).json() as any;
  assert.equal(both.states[0].tickets, 2);

  const only = await (await s.fetch(req("GET", "/states?boundary=only"))).json() as any;
  assert.equal(only.states[0].tickets, 1, "boundary=only did not narrow to the boundary ticket");
  assert.equal(only.states[0].total_ms, 999_000, "boundary=only returned the wrong ticket");
});

// ---- which intervals count -----------------------------------------

/** A ticket whose intervals are given explicitly, with categories. */
function withStates(key: string, day: number, spans: Array<[string, string, number]>) {
  const closed = T0 + day * DAY;
  let at = closed - 100_000;
  const intervals = spans.map(([state, category, ms]) => {
    const iv = { state, category, entered_at: at, left_at: at + ms };
    at += ms;
    return iv;
  });
  return { key, created_at: closed - 100_000, completed_at: closed, intervals };
}

// A ticket's time in a terminal state measures nothing -- `done` is not
// a duration, it is where the ticket stopped.
test("terminal states are excluded from the default aggregate", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["designing", "started", 1000],
      ["done", "completed", 50_000],
      ["duplicate", "duplicate", 60_000],
    ])],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.deepEqual(body.states.map((r: any) => r.state), ["designing"]);

  const all = await (await s.fetch(req("GET", "/states?includeTerminal=1"))).json() as any;
  assert.equal(all.states.length, 3, "includeTerminal=1 did not bring them back");
});

// BY CATEGORY, NOT BY NAME. Linear's built-in Duplicate has no protocol
// slug, and a terminal state added later would have none either -- an
// exclusion list of names would silently start counting it.
test("a terminal state nobody named is still excluded", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["designing", "started", 1000],
      ["shipped_to_orbit", "completed", 90_000],
    ])],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.deepEqual(body.states.map((r: any) => r.state), ["designing"],
    "an unrecognised terminal state was counted; the exclusion is matching names, not categories");
});

// backlog measures how early the ticket was foreseen; todo measures how
// long the OTHER tickets took. Both real, neither what a per-ticket
// average is asking about, and both dominate it if left in.
test("queue states are excluded from the default aggregate", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["backlog", "backlog", 500_000],
      ["todo", "unstarted", 400_000],
      ["designing", "started", 1000],
    ])],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.deepEqual(body.states.map((r: any) => r.state), ["designing"]);

  const all = await (await s.fetch(req("GET", "/states?includeQueue=1"))).json() as any;
  assert.equal(all.states.length, 3);
});

// The overnight hours land entirely in the states the author holds, so
// excluding those removes the sleep noise without encoding when anyone
// sleeps.
test("notStates subtracts from the defaults", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["designing", "started", 1000],
      ["design_review", "started", 400_000],
      ["blocked", "started", 300_000],
    ])],
  }));
  const body = await (await s.fetch(req("GET", "/states?notStates=design_review,blocked"))).json() as any;
  assert.deepEqual(body.states.map((r: any) => r.state), ["designing"]);
  assert.deepEqual(body.excluded.notStates, ["design_review", "blocked"]);
});

// An explicit list is the whole answer. Layering the defaults under it
// would mean asking for `done` and being handed nothing, with no way to
// tell that from a ticket that never got there.
test("an explicit states list overrides the defaults entirely", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["designing", "started", 1000],
      ["done", "completed", 50_000],
    ])],
  }));
  const body = await (await s.fetch(req("GET", "/states?states=done"))).json() as any;
  assert.equal(body.states.length, 1, "asking for done returned nothing; the defaults were layered under the list");
  assert.equal(body.states[0].state, "done");
  assert.equal(body.states[0].total_ms, 50_000);
});

// An empty bucket and a filtered one look identical in the numbers.
test("the response says what it filtered out", async () => {
  const s = store();
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.deepEqual(body.excluded.terminalCategories, ["completed", "canceled", "duplicate"]);
  assert.deepEqual(body.excluded.queueStates, ["backlog", "todo"]);
});

// The agent total reads the same filtered set as the per-state rows, or
// the two disagree about the same question.
test("the agent total respects the interval filter", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [withStates("ORC-1", 1, [
      ["designing", "started", 1000],
      ["in_progress", "started", 2000],
    ])],
  }));
  const all = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(all.agentTotal[0].total_ms, 3000);

  const less = await (await s.fetch(req("GET", "/states?notStates=in_progress"))).json() as any;
  assert.equal(less.agentTotal[0].total_ms, 1000,
    "the agent total ignored notStates; it and the per-state rows answer different questions");
});

test("agent states total separately from the per-state rows", async () => {
  const s = store();
  const closed = T0 + DAY;
  await s.fetch(req("POST", "/tickets", {
    tickets: [{
      key: "ORC-1",
      created_at: closed - 10_000,
      completed_at: closed,
      intervals: [
        { state: "designing", entered_at: closed - 9_000, left_at: closed - 6_000 },
        { state: "in_progress", entered_at: closed - 6_000, left_at: closed - 4_000 },
        // Blocked is a "started" state in the tracker but nothing runs
        // in it, so it must not land in the agent total.
        { state: "blocked", entered_at: closed - 4_000, left_at: closed },
      ],
    }],
  }));
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(body.agentTotal[0].total_ms, 5_000, "blocked time leaked into the agent total");
});

test("milestone windows select tickets by completion, not by assignment", async () => {
  const s = store();
  await s.fetch(req("POST", "/milestones", {
    milestones: [
      { name: "The engine", kind: "feature", window_start: T0, window_end: T0 + 3 * DAY },
      { name: "Tech debt · after", kind: "debt", window_start: T0 + 3 * DAY, window_end: T0 + 6 * DAY },
    ],
  }));
  await s.fetch(req("POST", "/tickets", {
    tickets: [ticket("ORC-1", 1, 1000), ticket("ORC-2", 2, 1000), ticket("ORC-3", 4, 1000)],
  }));
  const engine = await (await s.fetch(req("GET", "/states?milestone=The engine"))).json() as any;
  assert.equal(engine.states[0].tickets, 2);

  const debt = await (await s.fetch(req("GET", "/states?milestoneKind=debt"))).json() as any;
  assert.equal(debt.states[0].tickets, 1, "milestoneKind did not select by window");
});

test("work completed while a boundary was open is distinguishable", async () => {
  const s = store();
  await s.fetch(req("POST", "/milestones", {
    milestones: [{
      name: "The engine", kind: "feature",
      window_start: T0, window_end: T0 + 6 * DAY,
      boundary_ticket: "ORC-9",
      boundary_open_start: T0 + 3 * DAY, boundary_open_end: T0 + 5 * DAY,
    }],
  }));
  await s.fetch(req("POST", "/tickets", {
    tickets: [ticket("ORC-1", 1, 1000), ticket("ORC-2", 4, 1000)],
  }));
  const during = await (await s.fetch(req("GET", "/states?boundaryOpen=1"))).json() as any;
  assert.equal(during.states[0].tickets, 1);
});

test("bugs a milestone produced counts creation, not completion", async () => {
  const s = store();
  await s.fetch(req("POST", "/milestones", {
    milestones: [{ name: "M1", kind: "feature", window_start: T0, window_end: T0 + 2 * DAY }],
  }));
  // Created inside M1, completed well after it.
  await s.fetch(req("POST", "/tickets", {
    tickets: [{
      key: "ORC-1",
      created_at: T0 + DAY,
      completed_at: T0 + 10 * DAY,
      labels: ["bug"],
      intervals: [{ state: "designing", entered_at: T0 + DAY, left_at: T0 + DAY + 1000 }],
    }],
  }));
  const byCompletion = await (await s.fetch(req("GET", "/states?milestone=M1&hasLabels=bug"))).json() as any;
  assert.equal(byCompletion.states.length, 0, "completion-based grouping claimed the ticket");

  const byCreation = await (await s.fetch(req("GET", "/states?milestone=M1&hasLabels=bug&on=created"))).json() as any;
  assert.equal(byCreation.states[0].tickets, 1, "creation-based grouping missed the ticket");
});

test("buckets group by day and by week", async () => {
  const s = store();
  await s.fetch(req("POST", "/tickets", {
    tickets: [ticket("ORC-1", 0, 1000), ticket("ORC-2", 1, 1000), ticket("ORC-3", 8, 1000)],
  }));
  const day = await (await s.fetch(req("GET", "/states?bucket=day"))).json() as any;
  assert.equal(day.states.length, 3, "three days did not produce three buckets");

  const week = await (await s.fetch(req("GET", "/states?bucket=week"))).json() as any;
  assert.equal(week.states.length, 2, "two weeks did not produce two buckets");
});

// ---- runs ---------------------------------------------------------

function run(id: number, extra: Record<string, unknown> = {}) {
  return {
    run_id: id, repo: "swaggerallen/catapult", workflow: "ci.yml",
    started_at: T0 + DAY, duration_ms: 24_000, billable_ms: 60_000, job_count: 1,
    conclusion: "success", ...extra,
  };
}

// THE invariant. A run ages out of the host's API, and the row here
// then becomes the only record it ever ran. A later pass that can no
// longer see the run must not be able to overwrite it.
test("a run already stored is never overwritten", async () => {
  const s = store();
  await s.fetch(req("POST", "/runs", { runs: [run(1, { billable_ms: 60_000 })] }));
  const second = await s.fetch(req("POST", "/runs", {
    runs: [run(1, { billable_ms: 0, duration_ms: 0, conclusion: "" })],
  }));
  assert.equal((await second.json() as any).inserted, 0, "the re-send was counted as an insert");

  const body = await (await s.fetch(req("GET", "/minutes"))).json() as any;
  assert.equal(body.minutes[0].billable_ms, 60_000, "a later pass zeroed a stored run");
  assert.equal(body.minutes[0].runs, 1);
});

test("the store offers no way to delete a run", async () => {
  const s = store();
  await s.fetch(req("POST", "/runs", { runs: [run(1)] }));
  for (const path of ["/runs", "/runs/1", "/run/1"]) {
    const res = await s.fetch(req("DELETE", path));
    assert.equal(res.status, 404, `DELETE ${path} was not refused`);
  }
  const body = await (await s.fetch(req("GET", "/minutes"))).json() as any;
  assert.equal(body.minutes[0].runs, 1, "a run went missing");
});

// Most minutes belong to workflows that never touch a ticket. Dropping
// them would mean optimising a minority of the bill.
test("a run with no ticket is stored and counted", async () => {
  const s = store();
  await s.fetch(req("POST", "/runs", {
    runs: [run(1), run(2, { workflow: "pipeline-agent-dev.yml", kind: "dev", ticket_key: "ORC-1" })],
  }));
  const body = await (await s.fetch(req("GET", "/minutes"))).json() as any;
  const total = body.minutes.reduce((n: number, r: any) => n + r.runs, 0);
  assert.equal(total, 2, "the ticketless run was dropped");
});

test("rounding waste is the gap between billed and elapsed", async () => {
  const s = store();
  // Three 24-second sweeps: 72s elapsed, 3 minutes billed.
  await s.fetch(req("POST", "/runs", {
    runs: [1, 2, 3].map((i) => run(i, { workflow: "pipeline-sweep.yml", duration_ms: 24_000, billable_ms: 60_000 })),
  }));
  const body = await (await s.fetch(req("GET", "/minutes"))).json() as any;
  assert.equal(body.minutes[0].duration_ms, 72_000);
  assert.equal(body.minutes[0].billable_ms, 180_000);
  assert.equal(body.minutes[0].rounding_ms, 108_000, "rounding waste was not reported");
});

test("the collector's own runs are stored but excluded unless asked for", async () => {
  const s = store();
  await s.fetch(req("POST", "/runs", {
    runs: [run(1), run(2, { workflow: "pipeline-stats.yml", is_stats_job: true })],
  }));
  const off = await (await s.fetch(req("GET", "/minutes"))).json() as any;
  assert.equal(off.minutes.reduce((n: number, r: any) => n + r.runs, 0), 1);

  const on = await (await s.fetch(req("GET", "/minutes?statsJob=1"))).json() as any;
  assert.equal(on.minutes.reduce((n: number, r: any) => n + r.runs, 0), 2,
    "the collector's own runs were dropped rather than marked");
});

test("minutes group by kind as well as by workflow", async () => {
  const s = store();
  await s.fetch(req("POST", "/runs", {
    runs: [
      run(1, { workflow: "pipeline-agent-dev.yml", kind: "dev" }),
      run(2, { workflow: "pipeline-agent-design.yml", kind: "design" }),
      run(3, { workflow: "ci.yml", kind: "" }),
    ],
  }));
  const body = await (await s.fetch(req("GET", "/minutes?by=kind"))).json() as any;
  assert.equal(body.groupedBy, "kind");
  assert.deepEqual(body.minutes.map((r: any) => r.series).sort(), ["", "design", "dev"]);
});

test("the watermark round-trips so a backfill can resume", async () => {
  const s = store();
  await s.fetch(req("POST", "/watermark", {
    source: "github:swaggerallen/catapult", oldest_complete: T0, newest_seen: T0 + DAY,
  }));
  const body = await (await s.fetch(req("GET", "/watermark"))).json() as any;
  assert.equal(body[0].source, "github:swaggerallen/catapult");
  assert.equal(body[0].oldest_complete, T0);
});

test("a malformed write is refused whole rather than stored in part", async () => {
  const s = store();
  const res = await s.fetch(req("POST", "/tickets", {
    tickets: [ticket("ORC-1", 1, 1000), { key: "ORC-2" }],
  }));
  assert.equal(res.status, 400);
  const body = await (await s.fetch(req("GET", "/states"))).json() as any;
  assert.equal(body.states.length, 0, "the valid half of a rejected batch was stored");
});
