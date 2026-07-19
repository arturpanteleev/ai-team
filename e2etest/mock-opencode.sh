#!/bin/bash
set -euo pipefail

MOCK_DIR="$(cd "$(dirname "$0")" && pwd)"
MODE=""

PROMPT=""
if [[ "${1:-}" == "run" && -n "${2:-}" ]]; then
  PROMPT="$2"
  shift 2
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --message-file)
      PROMPT_FILE="$2"
      PROMPT=$(cat "$PROMPT_FILE")
      shift 2
      ;;
    --mode)
      MODE="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "${PROMPT:-}" ]]; then
  echo "MOCK: no prompt provided" >&2
  exit 1
fi

MODE="${MODE:-${MOCK_MODE:-normal}}"

# extract agent name (first line: # name)
AGENT=$(echo "$PROMPT" | head -1 | sed 's/^# //')
# extract feature
FEATURE=$(echo "$PROMPT" | sed -n '/^## Фича$/,/^$/p' | tail -n +2 | head -1 | xargs)

echo "MOCK: agent=$AGENT feature=$FEATURE mode=$MODE" >&2

create_output() {
  local path="$1"
  local content="$2"
  mkdir -p "$(dirname "$path")"
  echo "$content" > "$path"
  echo "MOCK:   created $path" >&2
}

ARTIFACT_ROOT=".ai-team/artifacts"

# create outputs based on agent
if [[ "$AGENT" == "analyst" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/proposal.md" "# Proposal for $FEATURE\n\n## Goal\n$FEATURE"
    create_output "$ARTIFACT_ROOT/$FEATURE/specs/product/spec.md" "# Product spec for $FEATURE\n\n## Requirements\n- feature $FEATURE"
  fi

elif [[ "$AGENT" == "architect" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/design.md" "# Design for $FEATURE\n\n## Architecture\nSimple"
    create_output "$ARTIFACT_ROOT/$FEATURE/tasks.md" "# Tasks for $FEATURE\n\n- [ ] implement"
  fi

elif [[ "$AGENT" == "coder" ]]; then
  :

elif [[ "$AGENT" == "reviewer" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/review.md" "# Review for $FEATURE\n\n**Verdict:** APPROVED\n\n## Comments\n- Looks good"
  elif [[ "$MODE" == "rejected" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/review.md" "# Review for $FEATURE\n\n**Verdict:** CHANGES_REQUESTED\n\n## Issues\n- Needs fixes"
  fi

elif [[ "$AGENT" == "tester" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/test-report.md" "# Test Report for $FEATURE\n\n**Result:** PASS\n\n## Tests\n- all green"
  elif [[ "$MODE" == "fail" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/test-report.md" "# Test Report for $FEATURE\n\n**Result:** FAIL\n\n## Tests\n- test 1 failed"
  fi

elif [[ "$AGENT" == "verifier" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$ARTIFACT_ROOT/$FEATURE/verification.md" "# Verification Report for $FEATURE\n\n## Общий вердикт: APPROVED\n\n## Acceptance Criteria\n- ✅ All criteria passed\n\n## Self-review\n- No issues found\n\n## DoD Checklist\n- [x] Acceptance Criteria выполнены\n- [x] Реализация соответствует решению\n\n## Известные ограничения\n- None"
  fi

elif [[ "$AGENT" == "deployer" ]]; then
  review_file="$ARTIFACT_ROOT/$FEATURE/review.md"
  test_report_file="$ARTIFACT_ROOT/$FEATURE/test-report.md"

  if [[ -f "$review_file" ]]; then
    if ! grep -qi "APPROVED" "$review_file"; then
      echo "MOCK: deployer aborted - review not APPROVED" >&2
      exit 1
    fi
  fi

  if [[ -f "$test_report_file" ]]; then
    if ! grep -qi "PASS" "$test_report_file"; then
      echo "MOCK: deployer aborted - tests not PASS" >&2
      exit 1
    fi
  fi

  echo "MOCK: deployer approved - ready to commit" >&2
fi

exit 0
