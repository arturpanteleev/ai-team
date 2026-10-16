#!/bin/sh
# Демо-гейт для Docker-адаптера (docker_program@1, S08). Запускается внутри
# alpine; репозиторий смонтирован только для чтения в /gate/repo, путь результата
# уже переписан под контейнер. Протокол тот же, что у host-gate.sh.
#
#   check     fail, если в результате есть строка с FIXME
#   flaky     если в репозитории лежит файл .demo/docker-mode со словом crash —
#             техническая ошибка (код 4, без ответа), иначе как check
set -eu

mode=${1:-check}
input=$(cat)
artifact=$(printf '%s' "$input" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p' | head -n 1)
emit() { printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"%s"}\n' "$1" "$2"; }

if [ "$mode" = "flaky" ]; then
  if [ "$(cat /gate/repo/.demo/docker-mode 2>/dev/null || true)" = "crash" ]; then
    echo "demo docker gate: sidecar crashed" >&2
    exit 4
  fi
fi

if grep -q 'FIXME' "$artifact"; then emit fail "в результате остался маркер FIXME"; else emit pass "маркеров FIXME нет"; fi
