#!/usr/bin/env bash
# Пересъёмка скриншотов документации (docs/assets/screens/) на настоящем
# прогоне ai-team без LLM: агенты — заглушка e2etest/mock-opencode.sh,
# gh — заглушка, которая «создаёт» PR. Модель, ключи и сеть не нужны.
#
#   bash docs/demo/screenshots.sh
#
# Нужны: go, git, python3, Google Chrome или Chromium (путь можно задать в
# CHROME).
# Рабочий каталог — AI_TEAM_DEMO_DIR (по умолчанию /tmp/ai-team-demo), он
# пересоздаётся при каждом запуске.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${OUT:-$REPO/docs/assets/screens}"
WORK="${AI_TEAM_DEMO_DIR:-/tmp/ai-team-demo}"
rm -rf "$WORK"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd -P)"
export AI_TEAM_DEMO_DIR="$WORK"
PORT="${PORT:-18765}"
HELPER="$REPO/docs/demo/screenshots.py"
FEATURE=greet-by-name
TASK="Добавить приветствие по имени в CLI"

if [[ -z "${CHROME:-}" ]]; then
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
           "$(command -v google-chrome || true)" "$(command -v chromium || true)" "$(command -v chromium-browser || true)"; do
    if [[ -n "$c" && -x "$c" ]]; then CHROME="$c"; break; fi
  done
fi
[[ -n "${CHROME:-}" ]] || { echo "Не найден Chrome/Chromium: укажите путь в CHROME" >&2; exit 1; }

mkdir -p "$WORK/bin" "$WORK/shots" "$OUT"
WEB_PID=""
cleanup() { [[ -n "$WEB_PID" ]] && kill "$WEB_PID" 2>/dev/null || true; rm -rf "$WORK/chrome"; }
trap cleanup EXIT

# shot OUT.png URL WIDTH,HEIGHT — headless Chrome. Страницы дашборда держат
# открытые соединения, и Chrome не завершается сам: PNG к этому моменту уже
# записан, поэтому процесс останавливается через 15 секунд.
shot() {
  "$CHROME" --headless=new --hide-scrollbars --no-first-run --user-data-dir="$WORK/chrome" \
    --force-device-scale-factor=1 --window-size="$3" --virtual-time-budget=6000 \
    --screenshot="$1" "$2" >/dev/null 2>&1 &
  local pid=$! i
  for i in $(seq 1 15); do kill -0 "$pid" 2>/dev/null || return 0; sleep 1; done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

# term OUT.png TITLE ARGS... — окно терминала из ANSI-записи.
term() {
  local out="$1" title="$2" height; shift 2
  height="$(python3 -I "$HELPER" term "$WORK/shots/term.html" "$title" "$@")"
  shot "$out" "file://$WORK/shots/term.html" "1200,$height"
}

echo "→ Сборка ai-team и заглушек"
(cd "$REPO" && go build -o "$WORK/bin/ai-team" ./cmd/ai-team)
ln -s "$REPO/e2etest/mock-opencode.sh" "$WORK/bin/opencode"
cat >"$WORK/bin/gh" <<EOF
#!/bin/sh
# Заглушка GitHub CLI: авторизация есть, PR «создаётся» без сети.
if [ "\$1" = auth ] && [ "\$2" = status ]; then echo "Logged in (demo)"; exit 0; fi
if [ "\$1" = pr ] && [ "\$2" = view ]; then
  [ -f "$WORK/pr-created" ] || exit 1
  printf '{"url":"https://github.com/you/greeter/pull/1","state":"OPEN","baseRefName":"main","headRefName":"%s","headRefOid":"%s"}\n' "\$3" "\$(git rev-parse HEAD)"
  exit 0
fi
touch "$WORK/pr-created"
echo https://github.com/you/greeter/pull/1
EOF
chmod +x "$WORK/bin/gh"
export PATH="$WORK/bin:$PATH"

echo "→ Учебный проект greeter"
P="$WORK/greeter"
mkdir -p "$P/cmd/greeter"
cp "$REPO/e2etest/sample-project/go.mod" "$P/"
cp "$REPO/e2etest/sample-project/main.go" "$P/cmd/greeter/"
printf '# greeter\n\nМаленький CLI для учебника ai-team.\n' >"$P/README.md"
git -C "$WORK" init -q --bare origin.git
git -C "$P" init -q -b main
git -C "$P" config user.name "Demo User"
git -C "$P" config user.email "demo@example.com"
git -C "$P" add . && git -C "$P" commit -q -m "greeter: начальная версия"
git -C "$P" remote add origin "$WORK/origin.git"
git -C "$P" push -q -u origin main
(cd "$P" && ai-team init >/dev/null)

echo "→ Прогон до подтверждения плана"
set +e
(cd "$P" && python3 -I "$HELPER" tty "$WORK/run.ansi" ai-team run --feature "$FEATURE" --task "$TASK")
code=$?
set -e
[[ $code -eq 3 ]] || { echo "ожидался код 3 (ждёт подтверждения), получен $code" >&2; exit 1; }
SHA="$(python3 -I "$HELPER" sha "$WORK/run.ansi")"
RUN_ID="$(ls "$P/.ai-team/runs" | head -1)"

echo "→ Дашборд"
(cd "$P" && exec ai-team web --port "$PORT" >"$WORK/web.log" 2>&1) &
WEB_PID=$!
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:$PORT/api/pipelines" >/dev/null 2>&1 && break; sleep 0.5; done
PIPE_ID="$(curl -fsS "http://127.0.0.1:$PORT/api/pipelines" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')"
BASE="http://127.0.0.1:$PORT"

shot "$OUT/dashboard-waiting.png" "$BASE/" "1440,760"
shot "$OUT/pipeline-waiting.png" "$BASE/pipelines/$PIPE_ID" "1440,980"

echo "→ Подтверждение плана и поставка"
(cd "$P" && python3 -I "$HELPER" tty "$WORK/approve.ansi" ai-team run --resume "$RUN_ID" --approve-plan "$SHA")

shot "$OUT/dashboard.png" "$BASE/" "1440,760"
shot "$OUT/pipeline.png" "$BASE/pipelines/$PIPE_ID" "1440,2680"
# Список артефактов дашборд дочитывает после завершения прогона, поэтому ждём.
ART_PATH=""
for _ in $(seq 1 20); do
  ART_PATH="$(curl -fsS "$BASE/api/pipelines/$PIPE_ID/artifacts" | python3 -I -c '
import json, sys
paths = [a["path"] for a in json.load(sys.stdin)]
print(next((p for p in paths if p.endswith("/review.md")), ""))')"
  [[ -n "$ART_PATH" ]] && break
  sleep 0.5
done
[[ -n "$ART_PATH" ]] || { echo "дашборд не показал review.md" >&2; exit 1; }
shot "$OUT/artifact.png" "$BASE/artifacts/$RUN_ID/$ART_PATH" "1440,640"

echo "→ Отказ от поставки"
set +e
(cd "$P" && python3 -I "$HELPER" answer "$WORK/stop.ansi" n ai-team run --feature add-farewell --task "Добавить прощание")
set -e
STOP_ID="$(curl -fsS "$BASE/api/pipelines" | python3 -I -c 'import json,sys; print(next(p["id"] for p in json.load(sys.stdin) if p["feature"]=="add-farewell"))')"
shot "$OUT/pipeline-stopped.png" "$BASE/pipelines/$STOP_ID" "1440,900"

echo "→ Вход в облачном режиме"
(cd "$P" && AI_TEAM_AUTH_SECRET="demo-secret-for-screenshots-only-0123456789" exec ai-team web --port "$((PORT + 1))" >"$WORK/web-auth.log" 2>&1) &
AUTH_PID=$!
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:$((PORT + 1))/" >/dev/null 2>&1 && break; sleep 0.5; done
shot "$OUT/login.png" "http://127.0.0.1:$((PORT + 1))/" "1440,640"
kill "$AUTH_PID" 2>/dev/null || true

echo "→ Терминал"
term "$OUT/terminal-run.png" "ai-team run — этапы и план поставки" --brief \
  --cut '"file_digests"' '"commit_message"' \
  --drop 'Решение: ai-team decision' \
  --cmd "ai-team run --feature $FEATURE --task \"$TASK\"" "$WORK/run.ansi"
term "$OUT/terminal-approve.png" "ai-team run --resume — поставка после подтверждения" --brief \
  --cut '"file_digests"' '"commit_message"' \
  --cmd "ai-team run --resume $RUN_ID --approve-plan $SHA" "$WORK/approve.ansi"

echo "→ Проверки без модели (gate)"
set +e
(cd "$REPO" && python3 -I "$HELPER" tty "$WORK/gate.ansi" bash docs/demo/run-demo.sh)
set -e
PROMPT='ai-team $' term "$OUT/gate.png" "docs/demo/run-demo.sh — три сценария gate" \
  --cmd "bash docs/demo/run-demo.sh" "$WORK/gate.ansi"

echo "Готово: $OUT"
