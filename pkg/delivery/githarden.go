package delivery

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/gitsafe"
)

// hardenedGitArgs — локальное имя для gitsafe.Args: каждый git-вызов
// контроллера в доставке идёт с overrides, запрещающими git исполнять код,
// заданный содержимым репозитория (QS-04).
func hardenedGitArgs(args ...string) []string { return gitsafe.Args(args...) }

// executionConfigSuffixes — суффиксы config-ключей, значение которых git
// исполняет как команду. Ключи вида `filter.<name>.clean` именуются
// пользователем, поэтому сопоставление идёт по префиксу секции и суффиксу
// последнего сегмента, а не по полному имени.
var executionConfigSuffixes = []struct{ prefix, suffix string }{
	{"filter.", ".clean"},
	{"filter.", ".smudge"},
	{"filter.", ".process"},
	{"diff.", ".textconv"},
	{"diff.", ".command"},
	{"merge.", ".driver"},
	{"credential.", ".helper"},
	{"gpg.", ".program"},
	{"url.", ".insteadof"},
	{"url.", ".pushinsteadof"},
	{"remote.", ".uploadpack"},
	{"remote.", ".receivepack"},
	{"trailer.", ".command"},
	{"trailer.", ".cmd"},
}

// executionConfigKeys — ключи, исполняющие команду, у которых нет
// пользовательского сегмента в имени.
var executionConfigKeys = map[string]bool{
	"core.hookspath": true, "core.fsmonitor": true, "core.sshcommand": true,
	"core.gitproxy": true, "core.pager": true, "core.editor": true,
	"core.askpass": true, "core.alternaterefscommand": true,
	"sequence.editor": true, "diff.external": true, "credential.helper": true,
	"gpg.program": true, "protocol.allow": true,
}

// executionConfigKey отвечает, может ли значение этого ключа быть запущено
// git как команда.
func executionConfigKey(key string) bool {
	key = strings.ToLower(key)
	if executionConfigKeys[key] {
		return true
	}
	if strings.HasPrefix(key, "protocol.") && strings.HasSuffix(key, ".allow") {
		return true
	}
	for _, rule := range executionConfigSuffixes {
		if strings.HasPrefix(key, rule.prefix) && strings.HasSuffix(key, rule.suffix) {
			return true
		}
	}
	return false
}

// repoScopedExecutionKeys разбирает вывод
// `git config --list --show-origin --show-scope -z` и возвращает имена
// ключей класса «git исполнит это как команду», пришедших из самого
// репозитория (scope local/worktree — включая подключённые через
// include.path файлы: git раскрывает include и сохраняет scope).
//
// Возвращаются ТОЛЬКО имена: значения таких ключей регулярно содержат
// секреты (credential.helper, url.<token>@host.insteadOf) и в манифест
// попадать не должны.
func repoScopedExecutionKeys(output string) []string {
	fields := strings.Split(output, "\x00")
	var keys []string
	seen := map[string]bool{}
	// Запись: scope \0 origin \0 "key\nvalue" \0.
	for index := 0; index+2 < len(fields); index += 3 {
		scope := fields[index]
		if scope != "local" && scope != "worktree" {
			continue
		}
		key, _, _ := strings.Cut(fields[index+2], "\n")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || seen[key] || !executionConfigKey(key) {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// presentHookNames перечисляет исполняемые файлы в каталоге hooks (без
// образцов *.sample, которые git кладёт сам и никогда не запускает).
func presentHookNames(hooksDir string) []string {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Mode()&0o111 == 0 {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// recordGitExecutionSurface фиксирует в манифесте доставки всё, чем этот
// репозиторий МОГ БЫ заставить git выполнить постороннюю команду: имена
// repo-scoped config-ключей соответствующего класса и имена присутствующих
// hook-файлов.
//
// Это запись, а не контроль: исполнение уже закрыто overrides из gitsafe
// (hooks, fsmonitor, ext::), а остальные ключи — core.sshCommand,
// gpg.program, credential.helper, filter.<name>.clean — у настоящих
// пользователей бывают осмысленной настройкой репозитория (per-repo deploy
// key, git-lfs), поэтому запрещать их наличие значило бы ломать легитимные
// репозитории. Шаг закрывает ровно ту дыру, на которую указывает QS-04:
// подсаженный .git/hooks/pre-push не попадал ни в mutation scope (`.git`
// исключён из workspace digest), ни в evidence — и оставался невидимым.
//
// Шаг никогда не отказывает в доставке: он наблюдатель, и отказ по его
// собственной ошибке дал бы агенту способ остановить доставку.
func recordGitExecutionSurface(ctx context.Context, target string, record func(StepResult) error) error {
	var findings []string
	if output, err := boundedGitOutput(ctx, target, "config", "--list", "--show-origin", "--show-scope", "-z"); err == nil {
		if keys := repoScopedExecutionKeys(string(output)); len(keys) > 0 {
			findings = append(findings, "repo-scoped config: "+strings.Join(keys, ", "))
		}
	} else {
		findings = append(findings, "repo-scoped config: не перечислен ("+err.Error()+")")
	}
	if hooks := presentHookNames(hooksDirectory(ctx, target)); len(hooks) > 0 {
		findings = append(findings, "hooks: "+strings.Join(hooks, ", "))
	}
	reason := "репозиторий не содержит config-ключей и hooks, исполняющих команды"
	if len(findings) > 0 {
		reason = "не исполнено контроллером — " + strings.Join(findings, "; ")
	}
	return record(successStep("inspect_git_execution_surface", reason))
}

// hooksDirectory возвращает каталог hooks по умолчанию. Считается он от
// common dir, а не через `--git-path hooks`: последний учитывает
// core.hooksPath, который мы сами перекрываем на /dev/null, и вернул бы нашу
// же заглушку вместо содержимого репозитория. Перенос каталога через
// core.hooksPath виден отдельно — как repo-scoped ключ в том же шаге.
func hooksDirectory(ctx context.Context, target string) string {
	fallback := filepath.Join(target, ".git", "hooks")
	output, err := boundedGitOutput(ctx, target, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fallback
	}
	common := strings.TrimSpace(string(output))
	if common == "" {
		return fallback
	}
	return filepath.Join(common, "hooks")
}
