#!/usr/bin/env bash
# setup.sh — подготовка локального окружения демонстрации S17.
#
#   bash scripts/demo/setup.sh <DEMO_HOME>
#
# Создаёт (идемпотентно: пересоздаёт каталог целиком):
#   <DEMO_HOME>/bin        мок opencode, заглушка gh, копия mock-opencode.sh
#   <DEMO_HOME>/state      режимы мока, лог вызовов gh, bare-remote origin.git
#   <DEMO_HOME>/repo       демо-репозиторий проекта с .ai-team/config.yaml
#   <DEMO_HOME>/ai-team    собранный бинарник
# Секреты и сеть не нужны; образ alpine из конфигурации Docker скачает сам.
set -euo pipefail

DEMO_HOME="${1:?usage: setup.sh <DEMO_HOME>}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"

# Каталог пересоздаётся только если он пуст или создан демо (маркер .ai-team-demo);
# пустое значение, «/», $HOME и его предки отклоняются (lib/guard.sh).
# shellcheck source=lib/guard.sh
source "$HERE/lib/guard.sh"
demo_prepare_home "$DEMO_HOME"
DEMO_HOME="$(demo_guard_path "$DEMO_HOME")"
mkdir -p "$DEMO_HOME/bin" "$DEMO_HOME/state" "$DEMO_HOME/repo"

echo "⟶ Сборка ai-team"
# Тулчейн — пин из .tool-versions, как в Makefile (явный GOTOOLCHAIN в окружении главнее).
if [[ -z "${GOTOOLCHAIN:-}" ]]; then
  pin="$(awk '$1 == "golang" { print "go" $2 }' "$REPO_ROOT/.tool-versions" 2>/dev/null || true)"
  [[ -n "$pin" ]] && export GOTOOLCHAIN="$pin"
fi
(cd "$REPO_ROOT" && go build -o "$DEMO_HOME/ai-team" ./cmd/ai-team)
go_used="$(cd "$REPO_ROOT" && go env GOVERSION)"
echo "  собрано тулчейном $go_used"

echo "⟶ Мок рантайма и заглушка gh"
cp "$HERE/bin/opencode" "$HERE/bin/gh" "$DEMO_HOME/bin/"
cp "$REPO_ROOT/e2etest/mock-opencode.sh" "$DEMO_HOME/bin/mock-opencode.sh"
chmod +x "$DEMO_HOME"/bin/*

echo "⟶ Демо-репозиторий и локальный bare-remote"
REPO="$DEMO_HOME/repo"
cp -R "$HERE/fixtures/repo/." "$REPO/"
mkdir -p "$REPO/agent"
cp "$REPO_ROOT/e2etest/container-review-agent/review.sh" "$REPO/agent/review.sh"
chmod +x "$REPO"/scripts/*.sh "$REPO/agent/review.sh"
git -C "$REPO" init -q -b master
git -C "$REPO" config user.name "AI Team Demo"
git -C "$REPO" config user.email "demo@example.invalid"
git init -q --bare -b master "$DEMO_HOME/state/origin.git"
git -C "$REPO" remote add origin "$DEMO_HOME/state/origin.git"

echo "⟶ Конфигурация проекта и демо-агенты"
mkdir -p "$REPO/.ai-team/agents"
cp "$REPO_ROOT/docs/demo/demo-template.yaml" "$REPO/.ai-team/config.yaml"
cp -R "$HERE/fixtures/agents/." "$REPO/.ai-team/agents/"
git -C "$REPO" add -A
git -C "$REPO" commit -q -m "demo: initial project"
git -C "$REPO" push -q -u origin master

# Каталоги контроллера не должны пачкать рабочее дерево.
printf '.ai-team/\n.demo/\n' > "$REPO/.git/info/exclude"

# Обмен фейкового трекера с внешним миром (только для демо, см. pkg/tracker/fake.go).
mkdir -p "$DEMO_HOME/tracker"
: > "$DEMO_HOME/tracker/inbox.jsonl"
: > "$DEMO_HOME/tracker/outbox.jsonl"

echo "✓ окружение готово: $DEMO_HOME"
