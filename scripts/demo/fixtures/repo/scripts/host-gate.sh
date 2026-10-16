#!/bin/sh
# Демо-гейт внешней программы (протокол ai-team.gate/v1, S07). Читает JSON с
# stdin, печатает ровно один JSON-ответ. Режим — аргумент:
#
#   todo     fail, если в результате есть строка, начинающаяся с TODO
#   flaky    первый запуск — техническая ошибка (код 3, без ответа), дальше как todo;
#            признак «уже падал» — файл .demo/host-flaky-fired в корне репозитория
#   qa       fail, если в отчёте нет строки «Result: PASS»
set -eu

mode=${1:-todo}
input=$(cat)
field() { printf '%s' "$input" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -n 1; }
emit() { printf '{"protocol":"ai-team.gate/v1","verdict":"%s","reason":"%s"}\n' "$1" "$2"; }

artifact=$(field path)
repo=$(field repo_root)

case "$mode" in
  flaky)
    marker="$repo/.demo/host-flaky-fired"
    if [ ! -f "$marker" ]; then
      mkdir -p "$repo/.demo"
      : > "$marker"
      echo "demo host gate: backend temporarily unavailable" >&2
      exit 3
    fi
    mode=todo
    ;;
esac

case "$mode" in
  todo)
    if grep -q '^TODO' "$artifact"; then emit fail "в результате остались строки TODO"; else emit pass "строк TODO нет"; fi ;;
  qa)
    if grep -q 'Result: PASS' "$artifact"; then emit pass "отчёт содержит Result: PASS"; else emit fail "в отчёте нет строки Result: PASS"; fi ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
