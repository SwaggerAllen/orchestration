#!/usr/bin/env bash
# Re-vendor Chart.js. Run from the repo root:
#
#   worker/vendor/regenerate.sh 4.5.1
#
# Prints the digest it produced; that value must also appear as
# CHART_JS_SHA256 in chartjs.ts, and a test checks the two agree. The
# point of the digest is that the blob stays re-derivable — a vendored
# dependency nobody can reproduce from upstream is worse than a link,
# because it can be neither audited nor updated with confidence.
set -euo pipefail
VERSION="${1:?usage: regenerate.sh <chart.js version>}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
( cd "$TMP" && npm pack "chart.js@${VERSION}" >/dev/null )
tar xzf "$TMP/chart.js-${VERSION}.tgz" -C "$TMP"
SRC="$TMP/package/dist/chart.umd.min.js"
SHA="$(sha256sum "$SRC" | cut -d' ' -f1)"
echo "chart.js ${VERSION}  sha256 ${SHA}  bytes $(stat -c%s "$SRC")"
python3 - "$SRC" "$VERSION" "$SHA" <<'PY'
import base64, hashlib, sys, textwrap, pathlib
src, version, sha = sys.argv[1], sys.argv[2], sys.argv[3]
raw = pathlib.Path(src).read_bytes()
assert hashlib.sha256(raw).hexdigest() == sha
out = pathlib.Path("worker/vendor/chartjs.ts")
text = out.read_text()
head, _ = text.split("const ENCODED =", 1)
head = head.replace(
    head.split('export const CHART_JS_VERSION = "')[1].split('"')[0], version, 1)
head = head.replace(
    head.split("CHART_JS_SHA256 =\n  \"")[1].split('"')[0], sha, 1)
lines = textwrap.wrap(base64.b64encode(raw).decode(), 100)
body = "\n".join(f'  "{l}" +' for l in lines[:-1]) + f'\n  "{lines[-1]}";'
out.write_text(head + "const ENCODED =\n" + body +
               "\n\n/** The library source, decoded once per isolate. */\n"
               "export const CHART_JS: string = atob(ENCODED);\n")
print("wrote", out)
PY
