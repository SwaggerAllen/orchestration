/**
 * A real SQLite behind Cloudflare's SqlStorage shape, for tests.
 *
 * TEST-ONLY. Nothing in the deployed Worker imports this — the runtime
 * supplies the real SqlStorage. It exists because the alternative is a
 * hand-written fake, and the thing under test here is the aggregation
 * itself: a fake would have to reimplement GROUP BY, strftime, EXISTS
 * and NOT EXISTS in order to answer, and would then be agreeing with
 * the code under test while both disagreed with SQLite. That is the
 * `tracker.Memory` failure exactly — a fake enumerating what the real
 * system does is a claim about the real system.
 *
 * node:sqlite ships with Node 22 and needs no flag (it warns that it is
 * experimental). No dependency is added and the gate line is unchanged.
 */
import { DatabaseSync } from "node:sqlite";

import type { SqlLike } from "./projectstats.ts";

export function memorySql(): SqlLike {
  const db = new DatabaseSync(":memory:");
  return {
    exec(query: string, ...bindings: unknown[]): Iterable<Record<string, unknown>> {
      // Cloudflare's exec takes statement and bindings together and
      // returns a cursor for either shape. node:sqlite splits them, and
      // refuses multi-statement SQL through prepare() — which is what
      // the CREATE TABLE blocks are, so they route to db.exec().
      const isQuery = /^\s*(select|with)\b/i.test(query);
      if (isQuery) {
        return db.prepare(query).all(...(bindings as never[])) as Record<string, unknown>[];
      }
      if (bindings.length === 0) {
        db.exec(query);
        return [];
      }
      db.prepare(query).run(...(bindings as never[]));
      return [];
    },
  };
}
