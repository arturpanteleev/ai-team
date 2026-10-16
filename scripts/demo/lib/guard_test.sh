#!/usr/bin/env bash
# guard_test.sh — проверка защиты каталога демо: bash scripts/demo/lib/guard_test.sh
set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=guard.sh
source "$HERE/guard.sh"
fail=0
expect() { # expect <ожидаемый код> <описание> <команда...>
  local want="$1" what="$2"; shift 2
  "$@" >/dev/null 2>&1; local got=$?
  if [[ "$got" -ne "$want" ]]; then echo "FAIL: $what (код $got, ожидался $want)"; fail=1; else echo "ok: $what"; fi
}

expect 1 "пустое значение отклонено" demo_prepare_home ""
expect 1 "корень отклонён" demo_prepare_home "/"
expect 1 "\$HOME отклонён" demo_prepare_home "$HOME"
expect 1 "родитель \$HOME отклонён" demo_prepare_home "$(dirname "$HOME")"
expect 1 "относительный путь отклонён" demo_prepare_home "relative/path"
expect 1 "удаление пустого значения отклонено" demo_remove_home ""

scratch="$(mktemp -d "${TMPDIR:-/tmp}/ai-team-demo-guardtest.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
mkdir "$scratch/foreign" && : > "$scratch/foreign/precious"
expect 1 "непустой чужой каталог не пересоздаётся" demo_prepare_home "$scratch/foreign"
expect 1 "чужой каталог без маркера не удаляется" demo_remove_home "$scratch/foreign"
[[ -f "$scratch/foreign/precious" ]] || { echo "FAIL: чужие данные удалены"; fail=1; }

expect 0 "новый каталог создаётся с маркером" demo_prepare_home "$scratch/demo"
[[ -f "$scratch/demo/$DEMO_MARKER" ]] || { echo "FAIL: нет маркера"; fail=1; }
: > "$scratch/demo/leftover"
expect 0 "каталог с маркером пересоздаётся" demo_prepare_home "$scratch/demo"
[[ ! -e "$scratch/demo/leftover" && -f "$scratch/demo/$DEMO_MARKER" ]] || { echo "FAIL: пересоздание"; fail=1; }
expect 0 "каталог с маркером удаляется" demo_remove_home "$scratch/demo"
[[ ! -e "$scratch/demo" ]] || { echo "FAIL: каталог остался"; fail=1; }

exit "$fail"
