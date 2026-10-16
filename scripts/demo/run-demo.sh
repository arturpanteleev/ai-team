#!/usr/bin/env bash
# run-demo.sh — воспроизводимая локальная демонстрация S17 одной командой.
#
#   bash scripts/demo/run-demo.sh
#
# Что делает: собирает ai-team, поднимает временный проект и настоящий
# `ai-team web` на 127.0.0.1, проходит сценарий через Web UI (Playwright), затем
# повторяет ключевые действия по сетевому MCP и доводит цикл код-ревью.
# Скриншоты — docs/demo/screens/. Ничего не публикует наружу; секретов не нужно;
# сеть нужна только для `docker pull` закреплённого образа alpine (один раз).
#
# Переменные: DEMO_HOME (каталог окружения, по умолчанию временный), DEMO_PORT
# (по умолчанию 18080), DEMO_KEEP=1 (не удалять окружение и не гасить сервер),
# DEMO_SKIP_UI=1 / DEMO_SKIP_MCP=1 (пропустить часть), DEMO_REAL_CLIENTS=1
# (дополнительно пройти настоящими клиентами claude и codex, если они есть).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
IMAGE="alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
PORT="${DEMO_PORT:-18080}"
DEMO_HOME="${DEMO_HOME:-$(mktemp -d "${TMPDIR:-/tmp}/ai-team-demo-s17.XXXXXX")}"
SERVER_PID=""

need() { command -v "$1" >/dev/null 2>&1 || { echo "нужен $1 в PATH" >&2; exit 2; }; }
for tool in go git node docker; do need "$tool"; done
docker info >/dev/null 2>&1 || { echo "Docker-демон недоступен" >&2; exit 2; }

cleanup() {
  local code=$?
  if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
  # Контейнеры демо несут метки контроллера; после нормального прогона их нет.
  # shellcheck disable=SC2046
  docker rm -f $(docker ps -aq --filter label=ai-team.stage-attempt --filter label=ai-team.gate-attempt 2>/dev/null) >/dev/null 2>&1 || true
  if [[ "${DEMO_KEEP:-0}" == "1" ]]; then
    echo "окружение оставлено: $DEMO_HOME (сервер остановлен)"
  else
    rm -rf "$DEMO_HOME"
  fi
  exit "$code"
}
trap cleanup EXIT

if [[ ! -d "$REPO_ROOT/web/node_modules/@playwright" ]]; then
  echo "⟶ npm ci (web)"; (cd "$REPO_ROOT/web" && npm ci --silent)
fi

echo "⟶ docker pull закреплённого образа"
docker pull -q "$IMAGE" >/dev/null

bash "$HERE/setup.sh" "$DEMO_HOME"

echo "⟶ ai-team web на 127.0.0.1:$PORT"
(
  cd "$DEMO_HOME/repo"
  export PATH="$DEMO_HOME/bin:$PATH"
  export AI_TEAM_FAKE_TRACKER_DIR="$DEMO_HOME/tracker"
  exec "$DEMO_HOME/ai-team" web --target "$DEMO_HOME/repo" --port "$PORT" >"$DEMO_HOME/web.log" 2>&1
) &
SERVER_PID=$!
for _ in $(seq 1 60); do
  curl -fsS "http://127.0.0.1:$PORT/api/auth/config" >/dev/null 2>&1 && break
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$DEMO_HOME/web.log" >&2; echo "сервер не стартовал" >&2; exit 1; }
  sleep 0.5
done
curl -fsS "http://127.0.0.1:$PORT/api/version" || true; echo

status=0
if [[ "${DEMO_SKIP_UI:-0}" != "1" ]]; then
  echo; echo "═══ Web UI: основной сценарий"
  node "$HERE/run-ui.mjs" --home "$DEMO_HOME" --port "$PORT" --phase main || status=$?
fi
if [[ "${DEMO_SKIP_MCP:-0}" != "1" && $status -eq 0 ]]; then
  echo; echo "═══ Сетевой MCP: ключевые действия"
  node "$HERE/run-mcp.mjs" --home "$DEMO_HOME" --port "$PORT" || status=$?
fi
if [[ "${DEMO_SKIP_UI:-0}" != "1" && $status -eq 0 ]]; then
  echo; echo "═══ Web UI: цикл код-ревью (внешний и внутренний исполнитель)"
  node "$HERE/run-ui.mjs" --home "$DEMO_HOME" --port "$PORT" --phase loops || status=$?
fi
if [[ "${DEMO_REAL_CLIENTS:-0}" == "1" && $status -eq 0 ]]; then
  echo; echo "═══ Настоящие клиенты MCP (claude, codex)"
  bash "$HERE/run-real-clients.sh" "$DEMO_HOME" "$PORT" || status=$?
fi

if [[ $status -ne 0 ]]; then
  echo; echo "ДЕМО НЕ ПРОШЛО (код $status). Журнал сервера: $DEMO_HOME/web.log" >&2
  tail -n 30 "$DEMO_HOME/web.log" >&2 || true
  DEMO_KEEP=1
fi
exit $status
