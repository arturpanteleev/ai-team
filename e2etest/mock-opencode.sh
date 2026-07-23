#!/bin/bash
set -euo pipefail

# Mock opencode для e2e-тестов.
# Поддерживает вызовы: `opencode run [-m model] <prompt>` и `--message-file <f>`.
# Режим задаётся MOCK_MODE (normal|rejected|fail|blocked):
#   rejected — ТОЛЬКО reviewer выдаёт CHANGES_REQUESTED, остальные работают нормально
#   fail     — ТОЛЬКО tester выдаёт FAIL
#   blocked  — ТОЛЬКО analyst сигнализирует BLOCKED через status-файл
#
# Если задан MOCK_CAPTURE_ENV_DIR, mock дампит собственное полученное
# окружение в "$MOCK_CAPTURE_ENV_DIR/<agent>.env" перед обычной обработкой —
# так тесты могут доказать, что OpenCodeIsolationEnvironment (permission
# JSON, XDG_CONFIG_HOME, env allow-list) реально доходит до подпроцесса через
# настоящий exec.Command, а не только проверяется на уровне построения
# слайса в unit-тестах.

MODE="${MOCK_MODE:-normal}"
PROMPT=""
PROMPT_FILE=""

if [[ "${1:-}" == "run" ]]; then
  shift
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -m|--model)
        shift 2
        ;;
	  -f|--file)
	    PROMPT_FILE="$2"
	    shift 2
	    ;;
      *)
        PROMPT="$1"
        shift
        ;;
    esac
  done
fi

if [[ -n "$PROMPT_FILE" ]]; then
  PROMPT=$(cat "$PROMPT_FILE")
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --message-file)
      PROMPT=$(cat "$2")
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

# первая строка промпта: "# <agent>"
AGENT=$(echo "$PROMPT" | head -1 | sed 's/^# //')
FEATURE=$(echo "$PROMPT" | sed -n '/^## Фича$/,/^$/p' | tail -n +2 | head -1 | xargs)

echo "MOCK: agent=$AGENT feature=$FEATURE mode=$MODE" >&2

if [[ -n "${MOCK_CAPTURE_ENV_DIR:-}" ]]; then
  mkdir -p "$MOCK_CAPTURE_ENV_DIR"
  env > "$MOCK_CAPTURE_ENV_DIR/$AGENT.env"
fi

create_output() {
  local path="$1"
  local content="$2"
  mkdir -p "$(dirname "$path")"
  # printf '%b' — реальные переводы строк (verdict-маркеры line-anchored)
  printf '%b\n' "$content" > "$path"
  echo "MOCK:   created $path" >&2
}

ARTIFACT_ROOT=".ai-team/artifacts"

write_summary() {
  create_output "$ARTIFACT_ROOT/$FEATURE/.stage-summary/$AGENT.md" "Мок-этап $AGENT выполнен (mode=$MODE)"
}

case "$AGENT" in
  analyst)
    if [[ "$MODE" == "blocked" ]]; then
      create_output "$ARTIFACT_ROOT/$FEATURE/status/analyst.md" "**Status:** BLOCKED\n**Blocker:** требования противоречивы (mock)"
      exit 0
    fi
    create_output "$ARTIFACT_ROOT/$FEATURE/proposal.md" "# Proposal for $FEATURE\n\n## Goal\n$FEATURE"
    create_output "$ARTIFACT_ROOT/$FEATURE/specs/product/spec.md" "# Product spec for $FEATURE\n\n## Requirements\n- feature $FEATURE"
    write_summary
    ;;

  architect)
    create_output "$ARTIFACT_ROOT/$FEATURE/design.md" "# Design for $FEATURE\n\n## Architecture\nSimple"
    create_output "$ARTIFACT_ROOT/$FEATURE/tasks.md" "# Tasks for $FEATURE\n\n- [ ] implement"
    write_summary
    ;;

  coder)
    create_output "e2e_implementation.go" "package e2eimplementation\n\nconst Feature = \"$FEATURE\""
    write_summary
    ;;

  reviewer)
    if [[ "$MODE" == "rejected" ]]; then
      create_output "$ARTIFACT_ROOT/$FEATURE/review.md" "# Review for $FEATURE\n\n## Issues\n- Needs fixes\n\n**Verdict:** CHANGES_REQUESTED"
    else
      create_output "$ARTIFACT_ROOT/$FEATURE/review.md" "# Review for $FEATURE\n\n## Comments\n- Looks good\n\n**Verdict:** APPROVED"
    fi
    write_summary
    ;;

  tester)
    if [[ "$MODE" == "fail" ]]; then
      create_output "$ARTIFACT_ROOT/$FEATURE/test-report.md" "# Test Authoring Report for $FEATURE\n\n## Coverage\n- required scenario could not be implemented\n\n**Result:** FAIL"
    else
	  create_output "e2e_implementation_test.go" "package e2eimplementation\n\nimport \"testing\"\n\nfunc TestFeature(t *testing.T) {\n  if Feature == \"\" { t.Fatal(\"empty feature\") }\n}"
      create_output "$ARTIFACT_ROOT/$FEATURE/test-report.md" "# Test Authoring Report for $FEATURE\n\n## Coverage\n- TestE2EImplementation covers the acceptance scenario\n- execution evidence is controller-owned\n\n**Result:** PASS"
    fi
    write_summary
    ;;

  verifier)
    create_output "$ARTIFACT_ROOT/$FEATURE/verification.md" "# Verification Report for $FEATURE\n\n## Acceptance Criteria\n- ✅ All criteria passed\n\n## Self-review\n- No issues found\n\n## DoD Checklist\n- [x] Acceptance Criteria выполнены\n\n## Известные ограничения\n- None\n\n**Verdict:** APPROVED"
    write_summary
    ;;

  deployer)
    review_file="$ARTIFACT_ROOT/$FEATURE/review.md"
    test_report_file="$ARTIFACT_ROOT/$FEATURE/test-report.md"

    if [[ -f "$review_file" ]] && ! grep -q '^\*\*Verdict:\*\* APPROVED' "$review_file"; then
      echo "MOCK: deployer aborted - review not APPROVED" >&2
      exit 1
    fi
    if [[ -f "$test_report_file" ]] && ! grep -q '^\*\*Result:\*\* PASS' "$test_report_file"; then
      echo "MOCK: deployer aborted - tests not PASS" >&2
      exit 1
    fi
    echo "MOCK: deployer approved - ready to commit" >&2
    write_summary
    ;;
esac

exit 0
