#!/bin/sh
# Demo gate program for the ai-team JSON gate protocol (ai-team.gate/v1).
# Usage: demo-gate.sh MODE
#
# Reads the input document from stdin and prints exactly one result document to
# stdout. The modes show every way a gate run can end:
#
#   pass            valid result, verdict pass
#   fail            valid result, verdict fail (a content opinion, exit code 0)
#   not_applicable  valid result, verdict not_applicable
#   check           pass when the artifact file has no line starting with TODO,
#                   fail otherwise (a real, if tiny, check)
#   evidence        pass with one evidence file written to evidence_dir
#   crash           exits with code 3 without a result      -> execution=error
#   badjson         prints text that is not JSON            -> execution=error
#   twodocs         prints two result documents             -> execution=error
#   flood           prints more than the output limit       -> execution=error
#   lost            kills itself with SIGKILL               -> execution=error
#   hang            sleeps until the timeout                -> execution=timeout
#   leak            prints and returns the SECRET_TOKEN env value (redaction test)
set -eu

mode=${1:-pass}
input=$(cat)

field() { printf '%s' "$input" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -n 1; }

emit() { # verdict reason [evidence-json]
  if [ $# -ge 3 ]; then
    printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"%s","evidence":[%s]}\n' "$1" "$2" "$3"
  else
    printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"%s"}\n' "$1" "$2"
  fi
}

case "$mode" in
  pass) emit pass "all good" ;;
  fail) emit fail "section Risks is missing" ;;
  not_applicable) emit not_applicable "nothing to check" ;;
  check)
    artifact=$(field path)
    if grep -q '^TODO' "$artifact"; then emit fail "unfinished TODO lines"; else emit pass "no TODO lines"; fi
    ;;
  evidence)
    dir=$(field evidence_dir)
    printf 'checked 3 items\n' > "$dir/report.txt"
    emit pass "report attached" '{"kind":"report","path":"report.txt"}'
    ;;
  crash) echo "boom: cannot open database" >&2; exit 3 ;;
  badjson) echo "everything is fine, trust me" ;;
  twodocs) emit pass one; emit fail two ;;
  flood) yes "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" ;;
  lost) kill -9 $$ ;;
  hang) sleep 30 ;;
  leak)
    echo "debug: token=${SECRET_TOKEN:-unset}" >&2
    emit fail "rejected with credential ${SECRET_TOKEN:-unset}"
    ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
