#!/usr/bin/env bash
# Compares the C# port against the original Go implementation on the same .eml corpus.
#   1. `classify` of every email (decision, category, score, method) from an empty database
#   2. `eval` (accuracy, question rate, learning curve, misses)
# Both must be byte-identical. Needs the Go toolchain, the .NET SDK and python3. Run from anywhere:
#   csharp/tools/parity.sh [seed] [emails-per-category]
#
# Caveat: the Go side parses MIME with enmime, the C# side with MimeKit. On rare messages enmime
# mis-decodes valid UTF-8 ("revisiÃ³n"); MimeKit does not. The script counts those emails and, when
# the decoded text differs, a different `eval` is expected and reported as a warning, not a failure.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
SEED="${1:-42}"
PER="${2:-40}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

(cd "$ROOT" && go build -o "$TMP/mailcat-go" ./cmd/mailcat)
dotnet build -c Release "$ROOT/csharp/src/MailCat.Cli" -o "$TMP/cs" >/dev/null
CS=(dotnet "$TMP/cs/mailcat.dll")

"${CS[@]}" sample-data -out "$TMP/sample" -seed "$SEED" -per "$PER" >/dev/null

summary() { python3 -c '
import sys, json
for line in sys.stdin:
    o = json.loads(line)
    print(o["email_id"], o["decision"], o["category"], round(o["score"], 3), o["method"])'; }

"$TMP/mailcat-go" -db "$TMP/go.db" classify -json "$TMP/sample" | summary > "$TMP/go.classify"
"${CS[@]}" --db "$TMP/cs.db" classify -json "$TMP/sample" | summary > "$TMP/cs.classify"
"$TMP/mailcat-go" eval -data "$TMP/sample" > "$TMP/go.eval"
"${CS[@]}" eval -data "$TMP/sample" > "$TMP/cs.eval"

parse_diffs=$(python3 - "$TMP/go.db" "$TMP/cs.db" <<'PY'
import sqlite3, sys
load = lambda p: {r[0]: r[1:] for r in sqlite3.connect(p).execute("select id, subject, body_text from emails")}
g, c = load(sys.argv[1]), load(sys.argv[2])
print(sum(1 for k in g if g[k] != c.get(k)))
PY
)

status=0
diff -u "$TMP/go.classify" "$TMP/cs.classify" > "$TMP/classify.diff" && echo "classify: identical ($(wc -l < "$TMP/go.classify" | tr -d ' ') emails)" || { echo "classify: DIFFERENT"; head -20 "$TMP/classify.diff"; status=1; }
if diff -u "$TMP/go.eval" "$TMP/cs.eval" > "$TMP/eval.diff"; then
  echo "eval:     identical"
elif [ "$parse_diffs" -gt 0 ]; then
  echo "eval:     different, but $parse_diffs email(s) were decoded differently by enmime and MimeKit (expected)"
else
  echo "eval: DIFFERENT"; head -20 "$TMP/eval.diff"; status=1
fi
exit $status
