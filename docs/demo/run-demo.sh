#!/usr/bin/env bash
# run-demo.sh — автономное демо `ai-team gate → bundle → verify` (V0-7) на
# не-Go репозитории. Три независимых сценария от общего base-коммита:
#   1) PASS        — source + сменяющий тест, JUnit-отчёт зелёный
#   2) FAIL policy — source-правка БЕЗ теста (test_modify required)
#   3) FAIL junit  — тест есть, но JUnit-отчёт содержит failure
# Каждый сценарий строит self-contained bundle, который потом проходит verify.
#
# Требования: bash, git, Go 1.26+. Запуск из корня ai-team:
#   bash docs/demo/run-demo.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
WORK="$(mktemp -d /tmp/ai-team-demo.XXXXXX)"
trap 'rm -rf "${WORK}"' EXIT

GATE_YAML="$HERE/gate.yaml"
PASS_XML="$HERE/pytest-pass.xml"
FAIL_XML="$HERE/pytest-fail.xml"

echo "⟶ Сборка ai-team"
BIN="$WORK/bin/ai-team"
mkdir -p "$WORK/bin"
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/ai-team)
AI_TEAM_REF="$(cd "$REPO_ROOT" && git rev-parse --short HEAD)"
echo "  ai-team из ревизии ${AI_TEAM_REF}"

echo "⟶ Создание демо-репозитория (Python, не Go)"
DEMO="$WORK/demo-app"
mkdir -p "$DEMO/tests" "$DEMO/reports"
cp "$GATE_YAML" "$DEMO/gate.yaml"
cp "$PASS_XML" "$DEMO/reports/pytest-pass.xml"
cp "$FAIL_XML" "$DEMO/reports/pytest-fail.xml"
cat > "$DEMO/app.py" <<'PY'
def parse_v1(text: str) -> str:
    return text.strip()
PY
cat > "$DEMO/tests/test_app.py" <<'PY'
from app import parse_v1

def test_clean_text():
    assert parse_v1("  hi  ") == "hi"

def test_already_clean():
    assert parse_v1("hi") == "hi"
PY
git -C "$DEMO" init -q
git -C "$DEMO" config user.email demo@example.invalid
git -C "$DEMO" config user.name "Demo"
git -C "$DEMO" add -A
git -C "$DEMO" commit -q -m "demo base"
BASE="$(git -C "$DEMO" rev-parse HEAD)"

run_gate() {
  # usage: run_gate <name> <branch> <base>
  local name="$1" branch="$2" base="$3" code=0
  git -C "$DEMO" checkout -q "$branch"
  "$BIN" gate --target "$DEMO" --base "$base" --candidate HEAD --out "$WORK/bundle-$name" || code=$?
  echo "  → [${name}] gate exit $code"
  echo "  → [${name}] verify bundle:"
  "$BIN" verify "$WORK/bundle-$name"
  printf '%s=%s\n' "$name" "$code" >> "$WORK/codes"
}

echo "⟶ Сценарий 1: PASS (source + сменяющий тест, зелёный отчёт)"
git -C "$DEMO" checkout -q -b pass-fix
sed -i '' 's/return text.strip()/return text.strip() or ""/' "$DEMO/app.py"
cat > "$DEMO/tests/test_app.py" <<'PY'
from app import parse_v1

def test_clean_text():
    assert parse_v1("  hi  ") == "hi"

def test_already_clean():
    assert parse_v1("hi") == "hi"

def test_empty():
    assert parse_v1("   ") == ""
PY
git -C "$DEMO" add -A
git -C "$DEMO" commit -q -m "fix: парсинг пустой строки + тест"
run_gate pass pass-fix "$BASE"

echo "⟶ Сценарий 2: FAIL по test_modify (source-правка без теста)"
git -C "$DEMO" checkout -q -b policy-fail "$BASE"
sed -i '' 's/return text.strip()/return text.strip().lower()/' "$DEMO/app.py"
git -C "$DEMO" add app.py
git -C "$DEMO" commit -q -m "source-only изменение (нет тестов)"
run_gate policy policy-fail "$BASE"

echo "⟶ Сценарий 3: FAIL по junit-отчёту (XML авторитетнее exit code)"
git -C "$DEMO" checkout -q -b junit-fail "$BASE"
cp "$FAIL_XML" "$DEMO/reports/pytest-pass.xml"
cat > "$DEMO/tests/test_app.py" <<'PY'
from app import parse_v1

def test_lower():
    assert parse_v1("HI") == "hi"
PY
git -C "$DEMO" add -A
git -C "$DEMO" commit -q -m "тест есть, но отчёт содержит failure"
run_gate junit junit-fail "$BASE"

echo
PASS_CODE="$(sed -n 's/^pass=//p' "$WORK/codes")"
POLICY_CODE="$(sed -n 's/^policy=//p' "$WORK/codes")"
JUNIT_CODE="$(sed -n 's/^junit=//p' "$WORK/codes")"
echo "✓ Демо завершено: pass=${PASS_CODE} (0), policy-fail=${POLICY_CODE} (1), junit-fail=${JUNIT_CODE} (1)"
echo "  Все bundle прошли самодостаточную verify — см. docs/demo/README.md и ci-gate-demo.yaml"
echo "  ai-team закреплён за ревизией ${AI_TEAM_REF}"
if [ "${PASS_CODE}" != "0" ] || [ "${POLICY_CODE}" != "1" ] || [ "${JUNIT_CODE}" != "1" ]; then
  echo "✗ Демо НЕ прошло assertion: ожидались pass=0, policy=1, junit=1" >&2
  exit 1
fi