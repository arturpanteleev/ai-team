#!/usr/bin/env bash
# guard.sh — защита от удаления «не того» каталога демо. Подключается через source.
#
# Скрипты демо пересоздают и удаляют DEMO_HOME целиком. Значение приходит из
# окружения, поэтому пустая или неудачная переменная (DEMO_HOME=, DEMO_HOME=/,
# DEMO_HOME=$HOME) не должна превращаться в `rm -rf` чужих данных. Удалять можно
# только каталог, который сам демо создал и пометил файлом DEMO_MARKER.

DEMO_MARKER=".ai-team-demo"

# demo_guard_path <каталог>: проверяет, что путь вообще допустим как каталог демо
# (не пуст, не корень, не $HOME и не его предок), и печатает его физический вид.
demo_guard_path() {
  local path="${1:-}"
  if [[ -z "$path" ]]; then echo "DEMO_HOME пуст: отказ" >&2; return 1; fi
  if [[ "$path" != /* ]]; then echo "DEMO_HOME должен быть абсолютным путём: $path" >&2; return 1; fi
  local physical
  if [[ -d "$path" ]]; then physical="$(cd -P "$path" && pwd -P)"; else physical="$(cd -P "$(dirname "$path")" 2>/dev/null && pwd -P)/$(basename "$path")" || return 1; fi
  local home_physical=""
  [[ -n "${HOME:-}" && -d "$HOME" ]] && home_physical="$(cd -P "$HOME" && pwd -P)"
  case "$physical" in
    /|//|"")
      echo "DEMO_HOME указывает на корень файловой системы: отказ" >&2; return 1 ;;
  esac
  if [[ -n "$home_physical" ]]; then
    # $HOME и любой его предок (/Users, /) нельзя удалять; путь внутри $HOME допустим.
    if [[ "$physical" == "$home_physical" || "$home_physical" == "$physical"/* ]]; then
      echo "DEMO_HOME ($physical) совпадает с домашним каталогом или его предком: отказ" >&2; return 1
    fi
  fi
  printf '%s\n' "$physical"
}

# demo_prepare_home <каталог>: готовит пустой каталог демо с маркером. Существующий
# каталог пересоздаётся только если в нём есть маркер; непустой чужой каталог —
# отказ (его содержимое демо не создавало).
demo_prepare_home() {
  local physical
  physical="$(demo_guard_path "${1:-}")" || return 1
  if [[ -e "$physical" ]]; then
    if [[ -f "$physical/$DEMO_MARKER" ]]; then
      rm -rf "$physical"
    elif [[ -d "$physical" && -z "$(ls -A "$physical")" ]]; then
      :
    else
      echo "$physical существует и не создан демо (нет $DEMO_MARKER): отказ его удалять" >&2; return 1
    fi
  fi
  mkdir -p "$physical"
  : > "$physical/$DEMO_MARKER"
}

# demo_remove_home <каталог>: удаляет каталог демо, только если в нём маркер.
demo_remove_home() {
  local physical
  physical="$(demo_guard_path "${1:-}")" || return 1
  if [[ ! -f "$physical/$DEMO_MARKER" ]]; then
    echo "в $physical нет $DEMO_MARKER: каталог не удаляется" >&2; return 1
  fi
  rm -rf "$physical"
}
