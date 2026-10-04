#!/usr/bin/env bash
# The guard on §8.3 rule 4's isolation: this checkout carries the manual
# tests and no implementation.
#
# **A script rather than an inline `run:` block so it can be executed by a
# test.** Embedded in the action it could only be grepped for, and three
# probes proved that insufficient — hardcoding `tracked=0` disabled the
# whole guarantee and every assertion still passed, because the assertions
# were about the presence of text rather than the behaviour of the check.
# Which is this repo's own lesson arriving at the one place it had not
# been applied: a gate whose output nobody has read on a real corpus is a
# guess about that corpus.
#
# Run from the checkout's root. Exits non-zero, loudly, on either failure.
set -euo pipefail

# The one permitted extra, named literally rather than globbed so a second
# file cannot arrive under the same allowance. `manual-verdict` reads the
# tracker ids from it to file a rule 8 finding; it carries owned paths,
# gate commands, component paths and citation shorthands, and says nothing
# about what this branch changed — so it is not the implementation rule 4
# keeps away from the judge.
allowed='^pipeline\.config\.json$'

tests=$(git ls-files 'tests/manual/*' | wc -l | tr -d ' ')
others=$(git ls-files | grep -v '^tests/manual/' | grep -Ev "$allowed" || true)
other_count=$(printf '%s' "$others" | grep -c . || true)

echo "checkout: ${tests} test file(s), ${other_count} other tracked file(s)"

if [ "$tests" -eq 0 ]; then
  echo "::error::the sparse checkout produced no manual tests — the judge has nothing to read" >&2
  exit 1
fi

if [ "$other_count" -ne 0 ]; then
  echo "::error::the checkout carries ${other_count} file(s) outside tests/manual — the judge must not see the implementation (ops-free-pipeline.md §8.3 rule 4)" >&2
  printf '%s\n' "$others" | head -20 >&2
  exit 1
fi
