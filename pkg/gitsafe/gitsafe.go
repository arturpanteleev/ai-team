// Package gitsafe даёт единый набор аргументов и переменных окружения, с
// которыми контроллер запускает git против репозитория, содержимое которого
// писал агент.
//
// Почему это нужно. Git умеет исполнять команды, заданные самим репозиторием:
// файлы в `$GIT_DIR/hooks`, `core.hooksPath`, `core.fsmonitor`, `ext::`
// remote helper. Всё это лежит внутри `.git/`, который не входит ни в
// workspace digest, ни в mutation scope, поэтому агент может туда записать, а
// контроллер — исполнить (QS-04: подсаженный `.git/hooks/pre-push`
// исполнялся на `git push` во время доставки).
//
// Почему именно `-c` и `GIT_CONFIG_*`. Значение, переданное в командной
// строке через `-c`, побеждает любой config-файл, включая `.git/config`,
// который агент может переписать; `GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n`
// имеет тот же приоритет и наследуется дочерними git-процессами, которые
// запускает не сам контроллер, а, например, `gh`.
package gitsafe

import (
	"fmt"
	"strconv"
	"strings"
)

// overrides — config-ключи, которые контроллер принудительно выставляет в
// каждом своём вызове git. Список намеренно короткий: сюда попадает только
// то, что (а) реально позволяет репозиторию исполнить код в пути доставки и
// (б) может быть переопределено без изменения легитимного поведения чужого
// репозитория. Ключи вида core.sshCommand, gpg.program, credential.helper,
// filter.<name>.clean у настоящих пользователей встречаются как осмысленная
// настройка, поэтому они не перекрываются, а фиксируются в манифесте
// доставки (delivery.recordGitExecutionSurface).
var overrides = []struct{ key, value string }{
	// Hooks: подсаженный .git/hooks/pre-push исполнялся контроллером на
	// git push, а .git/hooks/post-checkout — на git switch и worktree add.
	// core.hooksPath из .git/config переносит hooks в произвольный каталог,
	// поэтому перекрывается именно ключ, а не только каталог по умолчанию.
	{"core.hooksPath", "/dev/null"},
	// core.fsmonitor — произвольная команда, которую git запускает при любом
	// обновлении индекса: add, diff --cached, ls-files --stage, check-attr,
	// diff-tree. Это кеш ускорения, отключение стоит только скорости.
	{"core.fsmonitor", ""},
	// ext:: remote helper исполняет shell-команду из remote url. По
	// умолчанию транспорт запрещён, но protocol.ext.allow=always в
	// .git/config снимает запрет; контроллеру ext:: не нужен никогда.
	{"protocol.ext.allow", "never"},
}

// Args возвращает args, предварённые `-c key=value` для всех overrides.
// Порядок top-level флагов git не важен, поэтому результат безопасно
// сочетать с `-C <dir>`, `--no-pager` и прочими.
func Args(args ...string) []string {
	result := make([]string, 0, len(args)+2*len(overrides))
	for _, override := range overrides {
		result = append(result, "-c", override.key+"="+override.value)
	}
	return append(result, args...)
}

// Env возвращает base без унаследованных GIT_CONFIG_* и с добавленными
// overrides. Нужен там, где git запускает не контроллер, а вызванный им
// инструмент (`gh`): туда `-c` не передать.
func Env(base []string) []string {
	result := make([]string, 0, len(base)+1+2*len(overrides))
	for _, entry := range base {
		if isGitConfigEnv(entry) {
			continue
		}
		result = append(result, entry)
	}
	result = append(result, "GIT_CONFIG_COUNT="+strconv.Itoa(len(overrides)))
	for index, override := range overrides {
		result = append(result,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, override.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, override.value),
		)
	}
	return result
}

// isGitConfigEnv отсекает унаследованные GIT_CONFIG_COUNT/KEY_n/VALUE_n: при
// дублировании переменной в Cmd.Env выигрывает не «последняя запись», а
// решение конкретной ОС, поэтому старые значения убираются явно.
func isGitConfigEnv(entry string) bool {
	name, _, ok := strings.Cut(entry, "=")
	if !ok {
		return false
	}
	return name == "GIT_CONFIG_COUNT" ||
		strings.HasPrefix(name, "GIT_CONFIG_KEY_") ||
		strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}
