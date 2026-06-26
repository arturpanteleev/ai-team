#!/bin/bash
set -euo pipefail

MOCK_DIR="$(cd "$(dirname "$0")" && pwd)"
MODE_FILE=""
MODE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --message-file)
      PROMPT_FILE="$2"
      shift 2
      ;;
    --mode)
      MODE_FILE="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "${PROMPT_FILE:-}" ]]; then
  echo "MOCK: --message-file is required" >&2
  exit 1
fi

if [[ ! -f "$PROMPT_FILE" ]]; then
  echo "MOCK: prompt file not found: $PROMPT_FILE" >&2
  exit 1
fi

# override from --mode or env
if [[ -n "$MODE_FILE" ]]; then
  MODE="$MODE_FILE"
fi
MODE="${MODE:-${MOCK_MODE:-normal}}"

# read prompt
PROMPT=$(cat "$PROMPT_FILE")

# extract agent name (first line: # name)
AGENT=$(echo "$PROMPT" | head -1 | sed 's/^# //')
# extract feature
FEATURE=$(echo "$PROMPT" | sed -n '/^## Фича$/,/^$/p' | tail -n +2 | head -1 | xargs)
# extract output paths from "## Ожидаемые результаты"
OUTPUT_LINES=$(echo "$PROMPT" | sed -n '/^## Ожидаемые результаты/,/^## /p' | grep '→' || true)

echo "MOCK: agent=$AGENT feature=$FEATURE mode=$MODE" >&2

create_output() {
  local path="$1"
  local content="$2"
  mkdir -p "$(dirname "$path")"
  echo "$content" > "$path"
  echo "MOCK:   created $path" >&2
}

# create outputs based on agent
if [[ "$AGENT" == "analyst" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$FEATURE/proposal.md" "# Proposal for $FEATURE\n\n## Goal\n$FEATURE"
    create_output "$FEATURE/specs/product/spec.md" "# Product spec for $FEATURE\n\n## Requirements\n- feature $FEATURE"
  fi

elif [[ "$AGENT" == "architect" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$FEATURE/design.md" "# Design for $FEATURE\n\n## Architecture\nSimple"
    create_output "$FEATURE/tasks.md" "# Tasks for $FEATURE\n\n- [ ] implement"
  fi

elif [[ "$AGENT" == "coder" ]]; then
  # coder creates code files - nothing to mock for artifact check
  :

elif [[ "$AGENT" == "reviewer" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$FEATURE/review.md" "# Review for $FEATURE\n\n**Verdict:** APPROVED\n\n## Comments\n- Looks good"
  elif [[ "$MODE" == "rejected" ]]; then
    create_output "$FEATURE/review.md" "# Review for $FEATURE\n\n**Verdict:** CHANGES_REQUESTED\n\n## Issues\n- Needs fixes"
  fi

elif [[ "$AGENT" == "tester" ]]; then
  if [[ "$MODE" == "normal" ]]; then
    create_output "$FEATURE/test-report.md" "# Test Report for $FEATURE\n\n**Result:** PASS\n\n## Tests\n- all green"
  elif [[ "$MODE" == "fail" ]]; then
    create_output "$FEATURE/test-report.md" "# Test Report for $FEATURE\n\n**Result:** FAIL\n\n## Failures\n- test 1 failed"
  fi

elif [[ "$AGENT" == "deployer" ]]; then
  review_file="$FEATURE/review.md"
  test_report_file="$FEATURE/test-report.md"

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
