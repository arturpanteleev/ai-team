#!/bin/sh
# Reproducible fixture of an external container review agent (S10).
#
# It runs inside a tiny image (alpine, pinned by digest) and follows the stage
# executor contract (pkg/stageexec, schema_version 1): one JSON request on
# stdin, one JSON result on stdout. It is a stand-in for any real container
# reviewer: it reads the declared input "diff" (mounted read-only) and answers
#
#   APPROVED           when the diff has no FIXME marker,
#   CHANGES_REQUESTED  when it has one.
#
# Flags (argv comes from the trusted configuration):
#   --fail        report status "error" (a technical failure, not a verdict)
#   --crash       exit with a non-zero code without any result
#   --garbage     print something that is not a JSON result
#   --mismatch    claim a verdict that disagrees with the written review
#   --file        write the review to /stage/out/review instead of "content"
#   --both        give the review as content AND as a file (ambiguous)
set -eu

mode=""
for arg in "$@"; do
  case "$arg" in
    --fail|--crash|--garbage|--mismatch|--file|--both) mode="$arg" ;;
  esac
done

request=$(cat)

case "$mode" in
  --crash) echo "reviewer crashed on purpose" >&2; exit 3 ;;
  --garbage) echo "this is not json"; exit 0 ;;
  --fail) printf '{"schema_version":1,"status":"error","error":"reviewer backend unavailable"}'; exit 0 ;;
esac

# The input path is the container path of the declared input named "diff".
diff_path=$(printf '%s' "$request" | sed -n 's/.*"name":"diff","path":"\([^"]*\)".*/\1/p')
if [ -z "$diff_path" ] || [ ! -r "$diff_path" ]; then
  printf '{"schema_version":1,"status":"error","error":"input diff is missing"}'
  exit 0
fi

markers=$(grep -c 'FIXME' "$diff_path" || true)
if [ "$markers" -gt 0 ]; then
  verdict="CHANGES_REQUESTED"
  finding="found $markers FIXME marker(s) in the diff"
else
  verdict="APPROVED"
  finding="no blocking findings"
fi

# Evidence of the sandbox the controller gave us: the input is read-only and
# the network namespace has no default route.
if ( : >>"$diff_path" ) 2>/dev/null; then input_writable=true; else input_writable=false; fi
if tail -n +2 /proc/net/route | grep -q '^[^	 ]*[	 ]*00000000'; then default_route=true; else default_route=false; fi

claimed="$verdict"
if [ "$mode" = "--mismatch" ]; then
  claimed="APPROVED"
  verdict="CHANGES_REQUESTED"
fi

body="# Code review\\n\\n**Verdict:** $verdict\\n\\n- $finding\\n"
meta="\"metadata\":{\"agent\":\"fixture-review\",\"agent_version\":\"1\",\"input_writable\":\"$input_writable\",\"default_route\":\"$default_route\"}"

case "$mode" in
  --file|--both)
    printf '# Code review\n\n**Verdict:** %s\n\n- %s\n' "$verdict" "$finding" >/stage/out/review
    ;;
esac
if [ "$mode" = "--file" ]; then
  printf '{"schema_version":1,"status":"ok","summary":"%s","verdict":"%s",%s}' "$finding" "$claimed" "$meta"
else
  printf '{"schema_version":1,"status":"ok","summary":"%s","verdict":"%s","outputs":{"review":{"content":"%s"}},%s}' "$finding" "$claimed" "$body" "$meta"
fi
