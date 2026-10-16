#!/usr/bin/env bash
# run-real-clients.sh — подключает настоящие клиенты MCP (Claude Code и Codex CLI)
# к локальному `ai-team web` и выполняет чтения и одно действие.
#
#   bash scripts/demo/run-real-clients.sh <DEMO_HOME> <PORT>
#
# Нужны установленные и авторизованные `claude` и `codex`. Если клиент не
# установлен или не может работать неинтерактивно (нет входа в аккаунт), скрипт
# честно сообщает об этом и не считает это успехом: код возврата 3 для такого
# клиента, остальные клиенты проверяются независимо.
set -uo pipefail

HOME_DIR="${1:?usage: run-real-clients.sh <DEMO_HOME> <PORT>}"
PORT="${2:-18080}"
URL="http://127.0.0.1:$PORT/mcp"
TOKEN="$(cat "$HOME_DIR/repo/.ai-team/web.token")"
OUT="$HOME_DIR/real-clients"
mkdir -p "$OUT"
status=0
# macOS без coreutils не имеет timeout: ограничиваем время через perl.
limit() { perl -e 'alarm shift; exec @ARGV' "$@"; }

if command -v claude >/dev/null 2>&1; then
  cat >"$OUT/claude-mcp.json" <<EOF
{"mcpServers":{"ai-team":{"type":"http","url":"$URL","headers":{"Authorization":"Bearer $TOKEN"}}}}
EOF
  echo "⟶ claude: whoami и list_projects"
  if limit 180 claude -p "Вызови инструменты ai-team whoami и list_projects и коротко перечисли результат." \
      --mcp-config "$OUT/claude-mcp.json" --strict-mcp-config \
      --allowedTools mcp__ai-team__whoami mcp__ai-team__list_projects </dev/null >"$OUT/claude.txt" 2>&1; then
    cat "$OUT/claude.txt"
  else
    echo "claude не смог выполнить запрос (см. $OUT/claude.txt):"; head -n 5 "$OUT/claude.txt"; status=3
  fi
  echo "⟶ claude: действие create_backlog_item (запись в журнал mcp_action)"
  if limit 180 claude -p "Вызови инструмент ai-team create_backlog_item с project_id=2, title='Идея из Claude', idea='Проверка записи по MCP из настоящего клиента', feature='claude-idea' и выведи один ответ сервера." \
      --mcp-config "$OUT/claude-mcp.json" --strict-mcp-config \
      --allowedTools mcp__ai-team__create_backlog_item </dev/null >"$OUT/claude-action.txt" 2>&1; then
    cat "$OUT/claude-action.txt"
  else
    echo "claude не смог выполнить действие (см. $OUT/claude-action.txt)"; status=3
  fi
else
  echo "claude не установлен: проверка не выполнена"; status=3
fi

if command -v codex >/dev/null 2>&1; then
  echo "⟶ codex: whoami"
  export AI_TEAM_MCP_TOKEN="$TOKEN"
  if limit 180 codex exec --skip-git-repo-check --sandbox read-only -c "mcp_servers.ai_team.url=\"$URL\"" -c 'mcp_servers.ai_team.bearer_token_env_var="AI_TEAM_MCP_TOKEN"' \
      "Вызови инструмент ai-team whoami и коротко перечисли результат." </dev/null >"$OUT/codex.txt" 2>&1; then
    cat "$OUT/codex.txt"
  else
    echo "codex не смог выполнить запрос (см. $OUT/codex.txt):"; head -n 5 "$OUT/codex.txt"; status=3
  fi
else
  echo "codex не установлен: проверка не выполнена"; status=3
fi
exit $status
