// Package docs guards against silent regressions in onboarding documentation:
// missing sections, a deleted glossary entry, or a broken cross-link would
// otherwise only be caught by a human re-reading the whole README.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, relPath string) string {
	t.Helper()
	content, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("чтение %s: %v", relPath, err)
	}
	return string(content)
}

func assertContainsAll(t *testing.T, haystack, path string, needles []string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Errorf("%s: ожидалась подстрока %q, но она отсутствует", path, needle)
		}
	}
}

func TestReadmeCoversRequiredSections(t *testing.T) {
	readme := readRepoFile(t, "../README.md")
	assertContainsAll(t, readme, "README.md", []string{
		"## Для кого этот инструмент",
		"## Предварительные требования и установка",
		"## Быстрый старт",
		"## Как поставить фичу от начала до конца",
		"## CLI-справочник",
		"## Конвейер и зоны ответственности",
		"## Конфигурация",
		"## Evals",
		"## Глоссарий",
		"## Граница безопасности",
		"## Разработка",
	})
}

func TestReadmeGlossaryCoversCoreTerms(t *testing.T) {
	readme := readRepoFile(t, "../README.md")
	assertContainsAll(t, readme, "README.md", []string{
		"**checkpoint**",
		"**verdict marker**",
		"**BLOCKED**",
		"**mutation scope**",
		"**candidate**",
		"**canonical delivery plan**",
		"**attempt / run**",
	})
}

func TestReadmeLinksToCompanionDocs(t *testing.T) {
	readme := readRepoFile(t, "../README.md")
	assertContainsAll(t, readme, "README.md", []string{
		"docs/ARCHITECTURE.md",
		"CONTRIBUTING.md",
		"SECURITY.md",
	})
}

func TestArchitectureDocCoversReferencedAnchors(t *testing.T) {
	// README ссылается на конкретные заголовки в ARCHITECTURE.md через
	// #anchor; если заголовок переименуют, ссылка молча сломается.
	arch := readRepoFile(t, "ARCHITECTURE.md")
	assertContainsAll(t, arch, "docs/ARCHITECTURE.md", []string{
		"## Deployer и canonical delivery plan",
		"## Layered agent registry",
		"## Evidence и наблюдаемость",
	})
}

func TestContributingCoversOpenSpecCycle(t *testing.T) {
	contributing := readRepoFile(t, "../CONTRIBUTING.md")
	assertContainsAll(t, contributing, "CONTRIBUTING.md", []string{
		"Explore", "Propose", "Design", "Specs", "Tasks", "Apply", "Archive",
	})
}

// TestContributingKeepsOpenSpecOptional — OpenSpec является инструментом, а не
// обязанностью, и это должно быть сказано явно: без такой формулировки
// контрибьютор читает раздел про цикл как предписание. Отдельно проверяется,
// что единственное сохранившееся обязательство (специфицированное поведение не
// расходится с openspec/specs) из документа не пропало.
func TestContributingKeepsOpenSpecOptional(t *testing.T) {
	contributing := readRepoFile(t, "../CONTRIBUTING.md")
	assertContainsAll(t, contributing, "CONTRIBUTING.md", []string{
		"инструмент, а не обязанность",
		"Что остаётся обязательным",
		"openspec/specs",
	})
}

// dispatcherAliases — ветки switch, которые НЕ являются самостоятельными
// командами: это flag-написания уже задокументированной команды `help`.
// Список исключений держим явным и коротким, чтобы любая новая ветка попадала
// в проверку по умолчанию (fail-closed), а не молча выпадала из неё.
var dispatcherAliases = map[string]bool{"--help": true, "-h": true}

var (
	caseLabelRe   = regexp.MustCompile(`^\tcase (.+):$`)
	quotedValueRe = regexp.MustCompile(`"([^"]+)"`)
	cliCommandRe  = regexp.MustCompile("`ai-team ([a-z][a-z-]*)")
)

// dispatcherCommands извлекает множество команд из самого диспетчера
// cmd/ai-team/main.go.
//
// ПОЧЕМУ разбор исходника, а не литеральный список в тесте: список,
// продублированный в тесте, разойдётся с кодом ровно так же, как разошёлся
// README, — сторож тогда охраняет сам себя. Вариант «прогнать `ai-team help`»
// отвергнут: он требует собранного бинарника (go build внутри docs-теста), то
// есть делает документационный тест зависимым от сборки и медленным, а
// печатаемая справка — это тоже текст, который может отстать от switch.
// Диспетчер же — единственное место, где команда становится исполнимой.
func dispatcherCommands(t *testing.T) map[string]bool {
	t.Helper()
	source := readRepoFile(t, "../cmd/ai-team/main.go")
	const switchHeader = "switch os.Args[1] {"
	start := strings.Index(source, switchHeader)
	if start < 0 {
		t.Fatalf("cmd/ai-team/main.go: не найден диспетчер %q", switchHeader)
	}
	commands := map[string]bool{}
	for _, line := range strings.Split(source[start:], "\n") {
		// gofmt гарантирует, что switch верхнего уровня внутри main()
		// закрывается строкой ровно из одного таба и скобки — это конец блока.
		if line == "\t}" {
			break
		}
		label := caseLabelRe.FindStringSubmatch(line)
		if label == nil {
			continue
		}
		for _, value := range quotedValueRe.FindAllStringSubmatch(label[1], -1) {
			if dispatcherAliases[value[1]] {
				continue
			}
			commands[value[1]] = true
		}
	}
	if len(commands) == 0 {
		t.Fatal("cmd/ai-team/main.go: не удалось извлечь ни одной команды из диспетчера")
	}
	return commands
}

// readmeCLICommands собирает команды из первой колонки таблицы раздела
// «CLI-справочник». Читаем только строки таблицы: прозаические упоминания
// (`ai-team run --resume ...` ниже по тексту) не считаются документированием
// команды в справочнике.
func readmeCLICommands(t *testing.T) map[string]bool {
	t.Helper()
	readme := readRepoFile(t, "../README.md")
	const header = "## CLI-справочник"
	start := strings.Index(readme, header)
	if start < 0 {
		t.Fatalf("README.md: не найден раздел %q", header)
	}
	section := readme[start:]
	if next := strings.Index(section[len(header):], "\n## "); next >= 0 {
		section = section[:len(header)+next]
	}
	commands := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for _, match := range cliCommandRe.FindAllStringSubmatch(cells[0], -1) {
			commands[match[1]] = true
		}
	}
	return commands
}

// TestReadmeCLIReferenceMatchesDispatcher — сторож против расхождения, из-за
// которого `usage`, `gc` и `scheduler-worker` отсутствовали в справочнике.
// Проверяем оба направления: README не должен описывать и несуществующие
// команды тоже.
func TestReadmeCLIReferenceMatchesDispatcher(t *testing.T) {
	dispatcher := dispatcherCommands(t)
	documented := readmeCLICommands(t)

	var missing, extra []string
	for command := range dispatcher {
		if !documented[command] {
			missing = append(missing, command)
		}
	}
	for command := range documented {
		if !dispatcher[command] {
			extra = append(extra, command)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("README.md «CLI-справочник»: команды есть в cmd/ai-team/main.go, но не описаны: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("README.md «CLI-справочник»: описаны команды, которых нет в диспетчере cmd/ai-team/main.go: %v", extra)
	}
}

var (
	goModVersionRe      = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`)
	toolVersionsGoRe    = regexp.MustCompile(`(?m)^golang (\d+\.\d+(?:\.\d+)?)$`)
	workflowGoVersionRe = regexp.MustCompile(`go-version:\s*'([^']+)'`)
	// Минимальная версия в прозе: «Go 1.26.5+», «| Go | 1.26.5+ |»,
	// «`go` (1.26.5+)». Допускаем до 12 нецифровых символов между словом и
	// версией, чтобы покрыть разделители таблицы, кавычки и скобки.
	docGoMinimumRe = regexp.MustCompile(`(?i)\bgo\b[^0-9\n]{0,12}(\d+\.\d+(?:\.\d+)?)\+`)
)

func parseVersion(t *testing.T, raw string) []int {
	t.Helper()
	parts := strings.Split(raw, ".")
	numbers := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		value, err := strconv.Atoi(parts[i])
		if err != nil {
			t.Fatalf("некорректная версия %q: %v", raw, err)
		}
		numbers[i] = value
	}
	return numbers
}

func versionLess(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// TestGoVersionsAgreeAcrossManifests — единственный источник МИНИМАЛЬНОЙ версии
// Go это go.mod, единственный источник ТОЧНОГО патча — .tool-versions. Тест
// связывает их с прозой документов и с пинами CI, чтобы расхождение вида
// «README: Go 1.26+, go.mod: 1.26.5, CI: 1.26.6» падало сразу, а не жило
// незамеченным.
func TestGoVersionsAgreeAcrossManifests(t *testing.T) {
	goMod := readRepoFile(t, "../go.mod")
	goModMatch := goModVersionRe.FindStringSubmatch(goMod)
	if goModMatch == nil {
		t.Fatal("go.mod: не найдена директива go <version>")
	}
	minimum := goModMatch[1]

	t.Run("документы называют минимум из go.mod", func(t *testing.T) {
		for _, path := range []string{"../README.md", "../CONTRIBUTING.md", "demo/README.md"} {
			content := readRepoFile(t, path)
			matches := docGoMinimumRe.FindAllStringSubmatch(content, -1)
			if len(matches) == 0 {
				t.Errorf("%s: не найдено ни одного требования вида «Go <version>+»", path)
				continue
			}
			for _, match := range matches {
				if match[1] != minimum {
					t.Errorf("%s: заявлено %q, а go.mod требует %s+", path, strings.TrimSpace(match[0]), minimum)
				}
			}
		}
	})

	toolVersions := readRepoFile(t, "../.tool-versions")
	toolMatch := toolVersionsGoRe.FindStringSubmatch(toolVersions)
	if toolMatch == nil {
		t.Fatal(".tool-versions: не найдена строка «golang <version>»")
	}
	pinned := toolMatch[1]

	t.Run("пин toolchain не ниже минимума go.mod", func(t *testing.T) {
		if versionLess(parseVersion(t, pinned), parseVersion(t, minimum)) {
			t.Errorf(".tool-versions: golang %s ниже минимума go.mod (%s)", pinned, minimum)
		}
	})

	t.Run("CI использует тот же пин", func(t *testing.T) {
		workflows, err := filepath.Glob("../.github/workflows/*.y*ml")
		if err != nil {
			t.Fatalf("поиск workflow-файлов: %v", err)
		}
		if len(workflows) == 0 {
			t.Fatal("не найдено ни одного workflow в .github/workflows")
		}
		for _, workflow := range workflows {
			content := readRepoFile(t, workflow)
			for _, match := range workflowGoVersionRe.FindAllStringSubmatch(content, -1) {
				if match[1] != pinned {
					t.Errorf("%s: go-version '%s' расходится с .tool-versions (golang %s)", workflow, match[1], pinned)
				}
			}
		}
	})
}

// TestContributingDescribesVerifyTarget — описание `make verify` в CONTRIBUTING
// должно перечислять то, что target действительно делает: раньше из него
// выпадали test-coverage и test-e2e, и контрибьютор не знал, что локальный
// verify гоняет ещё и coverage-гейт с E2E.
func TestContributingDescribesVerifyTarget(t *testing.T) {
	makefile := readRepoFile(t, "../Makefile")
	start := strings.Index(makefile, "\nverify:")
	if start < 0 {
		t.Fatal("Makefile: не найден target verify")
	}
	body := makefile[start+1:]
	if end := strings.Index(body, "\n\n"); end >= 0 {
		body = body[:end]
	}

	// Сверяем не весь документ, а раздел «Make-таргеты»: именно он описывает
	// состав verify (комментарий в code block + абзац прозой). Слово «lint»
	// встречается и в других разделах, поэтому глобальный поиск по файлу
	// пропустил бы ровно то расхождение, ради которого написан этот тест.
	contributing := readRepoFile(t, "../CONTRIBUTING.md")
	const section = "## Make-таргеты"
	sectionStart := strings.Index(contributing, section)
	if sectionStart < 0 {
		t.Fatalf("CONTRIBUTING.md: не найден раздел %q", section)
	}
	described := contributing[sectionStart:]
	if next := strings.Index(described[len(section):], "\n## "); next >= 0 {
		described = described[:len(section)+next]
	}

	// Ключ — токен тела target-а verify в Makefile, значение — то, что обязано
	// быть названо в разделе CONTRIBUTING. Проверка идёт только по тем шагам,
	// которые в Makefile реально есть: удалили шаг — требование отпадает само,
	// и тест не начинает требовать документировать несуществующее.
	required := map[string]string{
		"verify: specs":         "specs",
		"gofmt -l ":             "gofmt",
		"go mod verify":         "go mod verify",
		"go vet ./...":          "go vet",
		"govulncheck":           "govulncheck",
		"go test -race":         "race",
		"$(MAKE) test-coverage": "test-coverage",
		"$(MAKE) test-e2e":      "test-e2e",
		"npm audit":             "audit",
		"npm run lint":          "lint",
		"npm test":              "tests",
		"npm run build":         "build",
	}
	for makefileToken, docToken := range required {
		if !strings.Contains(body, makefileToken) {
			continue
		}
		if !strings.Contains(described, docToken) {
			t.Errorf("CONTRIBUTING.md, раздел «Make-таргеты»: описание `make verify` не упоминает %q (в Makefile есть %q)", docToken, makefileToken)
		}
	}
}
