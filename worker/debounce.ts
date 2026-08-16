/**
 * SweepDebounce — a trailing debounce, one instance per project repo.
 *
 * The metronome fires a sweep on every Linear and GitHub webhook, and
 * the events arrive in bursts: a state change, its comment, and its
 * label edit are three webhooks a second apart, and a reconcile pass
 * that merges produces several more. Catapult ran six sweeps in
 * ninety-nine seconds one evening. Actions bills each job rounded up to
 * the minute, so that burst cost six minutes for about twenty seconds
 * of work, and the six sweeps computed almost the same answer.
 *
 * Trailing rather than leading, deliberately. A leading edge fires on
 * the *first* event of a burst, which is the worst moment to read the
 * tracker: the burst is the tracker mid-change, and a snapshot taken
 * then is the one most likely to see a half-applied state. Waiting for
 * the quiet at the end costs a few seconds of latency and reads a
 * settled world.
 *
 * The window is short on purpose. This is not batching to save money at
 * the cost of responsiveness — it is removing duplicates of an answer
 * that has not changed. Five seconds covers the webhook fan-out of a
 * single logical event without making the pipeline feel slower than the
 * beat already is.
 *
 * Dropping a beat is safe in a way that is worth stating: the sweep is
 * convergent, so a beat that never runs costs latency and never
 * correctness. The hourly cron is the floor under all of it.
 */

const WINDOW_MS = 5_000;

export interface DebounceEnv {
  DISPATCH_TOKEN: string;
}

/** What the alarm needs to know to fire the sweep it was waiting for. */
interface Pending {
  repository: string;
  workflow: string;
  ref: string;
}

export class SweepDebounce {
  private state: DurableObjectState;
  private env: DebounceEnv;

  constructor(state: DurableObjectState, env: DebounceEnv) {
    this.state = state;
    this.env = env;
  }

  /**
   * Called once per webhook. The first request in a quiet period arms
   * an alarm; every request inside the window is recorded and does
   * nothing else.
   *
   * The alarm is never pushed back by later requests. A rolling window
   * would let a long enough burst defer the sweep indefinitely, which
   * turns "wait for quiet" into "wait for the end of the workday" on a
   * busy project. This one fires WINDOW_MS after the *first* event and
   * the next burst arms a fresh one.
   */
  async fetch(request: Request): Promise<Response> {
    const pending = (await request.json()) as Pending;
    await this.state.storage.put("pending", pending);

    const armed = await this.state.storage.getAlarm();
    if (armed !== null) {
      return new Response("coalesced\n", { status: 202 });
    }
    await this.state.storage.setAlarm(Date.now() + WINDOW_MS);
    return new Response("armed\n", { status: 202 });
  }

  async alarm(): Promise<void> {
    const pending = await this.state.storage.get<Pending>("pending");
    await this.state.storage.delete("pending");
    if (!pending) {
      return;
    }
    const url = `https://api.github.com/repos/${pending.repository}/actions/workflows/${pending.workflow}/dispatches`;
    const res = await fetch(url, {
      method: "POST",
      headers: {
        authorization: `Bearer ${this.env.DISPATCH_TOKEN}`,
        accept: "application/vnd.github+json",
        "content-type": "application/json",
        "user-agent": "pipeline-metronome",
      },
      body: JSON.stringify({ ref: pending.ref, inputs: {} }),
    });
    // Logged and dropped rather than retried, same as the direct path:
    // the next webhook or the hourly cron beats again, and the sweep is
    // convergent.
    if (res.status !== 204) {
      console.error(`metronome: ${pending.repository}: HTTP ${res.status} ${await res.text()}`);
    }
  }
}
