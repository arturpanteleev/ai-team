// Package docs guards against silent regressions in onboarding documentation:
// missing sections, a deleted glossary entry, or a broken cross-link would
// otherwise only be caught by a human re-reading the whole README.
package docs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

var cliCommandRe = regexp.MustCompile("`ai-team ([a-z][a-z-]*)")

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
//
// ПОЧЕМУ go/parser, а не регулярка по строкам: регулярка вида `^\tcase (.+):$`
// молча не видит gofmt-чистые формы — `case "x": // комментарий` и case-список,
// перенесённый на несколько строк, — то есть сторож был бы fail-open ровно для
// новой команды, ради которой написан. AST видит их одинаково и не зависит от
// отступов и переносов.
func dispatcherCommands(t *testing.T) map[string]bool {
	t.Helper()
	const sourcePath = "../cmd/ai-team/main.go"
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, sourcePath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("разбор %s: %v", sourcePath, err)
	}

	// Диспетчер опознаём по выражению switch, а не по позиции в файле.
	// Собираем ВСЕ совпадения: при втором таком switch «побеждал бы последний»
	// и сторож молча читал бы не тот блок, обвиняя README в несуществующем
	// расхождении. Неоднозначность — это отказ, а не выбор наугад.
	var dispatchers []*ast.SwitchStmt
	ast.Inspect(file, func(node ast.Node) bool {
		switchStmt, ok := node.(*ast.SwitchStmt)
		if !ok || switchStmt.Tag == nil {
			return true
		}
		if types.ExprString(switchStmt.Tag) == "os.Args[1]" {
			dispatchers = append(dispatchers, switchStmt)
			return false
		}
		return true
	})
	switch len(dispatchers) {
	case 0:
		t.Fatalf("%s: не найден switch по os.Args[1]", sourcePath)
	case 1:
	default:
		lines := make([]int, 0, len(dispatchers))
		for _, candidate := range dispatchers {
			lines = append(lines, fileSet.Position(candidate.Pos()).Line)
		}
		t.Fatalf("%s: найдено несколько switch по os.Args[1] (строки %v) — неясно, какой из них диспетчер", sourcePath, lines)
	}
	dispatcher := dispatchers[0]

	commands := map[string]bool{}
	for _, statement := range dispatcher.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok || clause.List == nil { // default — не команда
			continue
		}
		for _, expression := range clause.List {
			literal, ok := expression.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				// Не строковый литерал — сторож не может решить, команда это
				// или нет, поэтому падаем, а не пропускаем молча.
				t.Fatalf("%s:%d: ветка диспетчера не является строковым литералом: %s",
					sourcePath, fileSet.Position(expression.Pos()).Line, types.ExprString(expression))
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				t.Fatalf("%s: не разобрать литерал %s: %v", sourcePath, literal.Value, unquoteErr)
			}
			if dispatcherAliases[value] {
				continue
			}
			commands[value] = true
		}
	}
	if len(commands) == 0 {
		t.Fatalf("%s: не удалось извлечь ни одной команды из диспетчера", sourcePath)
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
	goModVersionRe   = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`)
	toolVersionsGoRe = regexp.MustCompile(`(?m)^golang (\d+\.\d+(?:\.\d+)?)$`)
	// Кавычки в YAML необязательны и стиль может смениться, поэтому
	// принимаем 'x', "x" и голое x: иначе сабтест стал бы вакуумным ровно
	// в тот момент, когда кто-то переформатирует workflow.
	workflowGoVersionRe = regexp.MustCompile(`go-version\s*:\s*["']?([0-9][^"'\s#]*)["']?`)
	// (?m) обязателен: без него `$` означает конец ВСЕГО файла, и пин в
	// середине workflow не нашёлся бы никогда — ветка go-version-file была бы
	// мёртвой, а миграция на неё (#136) падала бы с ложным «0 пинов».
	workflowGoFileRe  = regexp.MustCompile(`(?m)^\s*go-version-file\s*:\s*["']?([^"'\s#]+)["']?`)
	workflowSetupGoRe = regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses\s*:\s*actions/setup-go`)
	// Минимальная версия в прозе: «Go 1.26.5+», «| Go | 1.26.5+ |»,
	// «`go` (1.26.5+)», «Go 1.26.5+.» в комментарии шелл-скрипта. Допускаем до
	// 12 нецифровых символов между словом и версией — это покрывает
	// разделители таблицы, кавычки и скобки.
	//
	// Хвостовой «+» НЕ обязателен: «требуется Go 1.20» — такое же расхождение,
	// как «Go 1.20+», и именно его сторож с обязательным плюсом пропускал.
	docGoMinimumRe = regexp.MustCompile(`(?i)\bgo\b[^0-9\n]{0,12}(\d+\.\d+(?:\.\d+)?)\+?`)
)

// goVersionDocs — все файлы, которые называют минимальную версию Go читателю.
// Список включает не только Markdown: `docs/demo/run-demo.sh` объявляет
// требования в шапке скрипта и уже успел разойтись с go.mod именно потому, что
// сторожа смотрели только на .md.
var goVersionDocs = []string{
	"../README.md",
	"../CONTRIBUTING.md",
	"demo/README.md",
	"demo/run-demo.sh",
}

// goVersionWorkflowGlobs — все места, где версия Go пинится для CI. Демо-workflow
// лежит вне .github/workflows, но это такой же пин, и он уже был четвёртой
// копией версии.
var goVersionWorkflowGlobs = []string{
	"../.github/workflows/*.y*ml",
	"demo/*.y*ml",
}

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
		for _, path := range goVersionDocs {
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
		var workflows []string
		for _, pattern := range goVersionWorkflowGlobs {
			matched, err := filepath.Glob(pattern)
			if err != nil {
				t.Fatalf("поиск workflow по %q: %v", pattern, err)
			}
			workflows = append(workflows, matched...)
		}
		if len(workflows) == 0 {
			t.Fatal("не найдено ни одного workflow-файла")
		}
		checked := 0
		for _, workflow := range workflows {
			pins := parseWorkflowGoPins(readRepoFile(t, workflow))
			if pins.setupSteps == 0 {
				continue // workflow не ставит Go — пинить нечего
			}
			checked++
			for _, problem := range workflowPinProblems(pins, pinned) {
				t.Errorf("%s: %s", workflow, problem)
			}
		}
		if checked == 0 {
			t.Fatal("ни в одном workflow не найден шаг actions/setup-go — сторож стал вакуумным")
		}
	})
}

// workflowGoPins — пины Go, найденные в одном workflow.
type workflowGoPins struct {
	setupSteps int
	versions   []string
	files      []string
}

func parseWorkflowGoPins(content string) workflowGoPins {
	pins := workflowGoPins{setupSteps: len(workflowSetupGoRe.FindAllString(content, -1))}
	for _, match := range workflowGoVersionRe.FindAllStringSubmatch(content, -1) {
		pins.versions = append(pins.versions, match[1])
	}
	for _, match := range workflowGoFileRe.FindAllStringSubmatch(content, -1) {
		pins.files = append(pins.files, match[1])
	}
	return pins
}

// workflowPinProblems — чистая проверка пинов одного workflow против
// .tool-versions. Вынесена из сабтеста, чтобы саму логику сторожа можно было
// проверить тестом на синтетическом YAML: ветка go-version-file уже была
// мёртвой из-за `$` без (?m), и это никак не всплывало.
func workflowPinProblems(pins workflowGoPins, pinned string) []string {
	var problems []string
	// Шаг setup-go без пина берёт произвольную версию Go — это молчаливое
	// расхождение, поэтому требуем пин на каждый шаг.
	if len(pins.versions)+len(pins.files) < pins.setupSteps {
		problems = append(problems, fmt.Sprintf("%d шагов actions/setup-go, но только %d пинов go-version/go-version-file",
			pins.setupSteps, len(pins.versions)+len(pins.files)))
	}
	for _, version := range pins.versions {
		if version != pinned {
			problems = append(problems, fmt.Sprintf("go-version %q расходится с .tool-versions (golang %s)", version, pinned))
		}
	}
	for _, file := range pins.files {
		// go-version-file допустим только если указывает на манифест, который
		// этот тест и считает источником правды.
		if !strings.HasSuffix(file, ".tool-versions") && !strings.HasSuffix(file, "go.mod") {
			problems = append(problems, fmt.Sprintf("go-version-file %q не ссылается ни на .tool-versions, ни на go.mod", file))
		}
	}
	return problems
}

// TestWorkflowPinParsingCoversEveryPinForm — тест на сам сторож. Проверяет, что
// распознаются все формы записи пина (кавычки любые или отсутствуют,
// go-version-file), и что расхождение и пропуск пина действительно отвергаются.
// Без этого ветка go-version-file год могла бы быть мёртвой, а миграция на неё
// (#136) упиралась бы в ложное «0 пинов».
func TestWorkflowPinParsingCoversEveryPinForm(t *testing.T) {
	const step = "      - uses: actions/setup-go@d35c59abb061a4a6fb18e82ac0862c26744d6ab5 # v5.5.0\n        with:\n"
	const pinned = "1.26.6"

	cases := []struct {
		name    string
		content string
		accept  bool
	}{
		{"одинарные кавычки", step + "          go-version: '1.26.6'\n", true},
		{"двойные кавычки", step + "          go-version: \"1.26.6\"\n", true},
		{"без кавычек", step + "          go-version: 1.26.6\n", true},
		{"другая версия", step + "          go-version: '1.27.0'\n", false},
		{"пин отсутствует", step + "          check-latest: true\n", false},
		{"go-version-file на .tool-versions", step + "          go-version-file: .tool-versions\n", true},
		{"go-version-file на go.mod", step + "          go-version-file: go.mod\n", true},
		{"go-version-file на чужой файл", step + "          go-version-file: something-else\n", false},
		{"go-version-file не в последней строке", step + "          go-version-file: .tool-versions\n          check-latest: true\n      - run: go build ./...\n", true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pins := parseWorkflowGoPins(testCase.content)
			if pins.setupSteps != 1 {
				t.Fatalf("ожидался ровно один шаг actions/setup-go, найдено %d", pins.setupSteps)
			}
			problems := workflowPinProblems(pins, pinned)
			if testCase.accept && len(problems) > 0 {
				t.Errorf("форма должна приниматься, но сторож возразил: %v", problems)
			}
			if !testCase.accept && len(problems) == 0 {
				t.Error("форма должна отвергаться, но сторож промолчал")
			}
		})
	}
}

// verifyDescriptions возвращает два независимых описания `make verify` из
// CONTRIBUTING: комментарий в блоке «Make-таргеты» и абзац прозой. Оба обязаны
// быть полными — именно неполный комментарий при полном абзаце и был issue
// #115, поэтому проверяются они по отдельности, а не объединением.
//
// ПОЧЕМУ именно эти два фрагмента, а не весь раздел: в разделе есть
// самостоятельные строки `make test-coverage`, `make test-e2e`, `make specs`,
// и поиск токенов по всему разделу удовлетворялся бы ими — сторож проходил бы,
// даже если описание verify вообще удалить. Пробелы и переносы схлопываются,
// чтобы обычный reflow Markdown («go mod\nverify») не красил тест.
func verifyDescriptions(t *testing.T, contributing string) map[string]string {
	t.Helper()
	const section = "## Make-таргеты"
	sectionStart := strings.Index(contributing, section)
	if sectionStart < 0 {
		t.Fatalf("CONTRIBUTING.md: не найден раздел %q", section)
	}
	body := contributing[sectionStart:]
	if next := strings.Index(body[len(section):], "\n## "); next >= 0 {
		body = body[:len(section)+next]
	}

	var comment []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "make verify") {
			comment = append(comment, line)
			continue
		}
		if len(comment) == 0 {
			continue
		}
		// Продолжение многострочного комментария к той же цели.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			comment = append(comment, line)
			continue
		}
		break
	}
	if len(comment) == 0 {
		t.Fatal("CONTRIBUTING.md, раздел «Make-таргеты»: в блоке команд нет строки `make verify`")
	}

	prose := ""
	if start := strings.Index(body, "`make verify`"); start >= 0 {
		prose = body[start:]
		if end := strings.Index(prose, "\n\n"); end >= 0 {
			prose = prose[:end]
		}
	}
	if prose == "" {
		t.Fatal("CONTRIBUTING.md, раздел «Make-таргеты»: нет абзаца, описывающего `make verify`")
	}

	return map[string]string{
		"комментарий в блоке команд": normalizeSpace(strings.Join(comment, " ")),
		"абзац прозой":               normalizeSpace(prose),
	}
}

// normalizeSpace схлопывает пробелы и переносы: мягкий перенос Markdown не
// должен превращать «go mod verify» в ненайденный токен.
func normalizeSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
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
	target := makefile[start+1:]
	if end := strings.Index(target, "\n\n"); end >= 0 {
		target = target[:end]
	}

	descriptions := verifyDescriptions(t, readRepoFile(t, "../CONTRIBUTING.md"))

	// Ключ — токен тела target-а verify в Makefile, значение — допустимые
	// написания в документе (достаточно любого: у шага может быть и командное,
	// и человеческое имя). Проверка идёт только по тем шагам, которые в
	// Makefile реально есть: удалили шаг — требование отпадает само, и тест не
	// начинает требовать документировать несуществующее.
	//
	// Токены обязаны быть НЕПЕРЕКРЫВАЮЩИМИСЯ: голое "tests" удовлетворялось бы
	// словами «race tests» из соседней фразы, а голое "build" — любым
	// «go build», и требование про фронтенд становилось тавтологией. Поэтому
	// для npm-шагов берём написания, которые может дать только сам фронтенд-шаг.
	required := map[string][]string{
		"verify: specs":         {"specs", "OpenSpec"},
		"gofmt -l ":             {"gofmt"},
		"go mod verify":         {"go mod verify"},
		"go vet ./...":          {"go vet"},
		"govulncheck":           {"govulncheck"},
		"go test -race":         {"race"},
		"$(MAKE) test-coverage": {"test-coverage"},
		"$(MAKE) test-e2e":      {"test-e2e", "E2E"},
		"npm audit":             {"npm audit", "audit"},
		"npm run lint":          {"npm run lint", "lint"},
		"npm test":              {"npm test", "/tests", "тесты фронтенда", "frontend-тесты"},
		"npm run build":         {"npm run build", "tests/build", "сборку фронтенда", "frontend build"},
	}
	places := make([]string, 0, len(descriptions))
	for place := range descriptions {
		places = append(places, place)
	}
	sort.Strings(places)

	makefileTokens := make([]string, 0, len(required))
	for makefileToken := range required {
		makefileTokens = append(makefileTokens, makefileToken)
	}
	sort.Strings(makefileTokens)

	for _, makefileToken := range makefileTokens {
		if !strings.Contains(target, makefileToken) {
			continue
		}
		for _, place := range places {
			mentioned := false
			for _, docToken := range required[makefileToken] {
				if strings.Contains(descriptions[place], docToken) {
					mentioned = true
					break
				}
			}
			if !mentioned {
				t.Errorf("CONTRIBUTING.md, %s: описание `make verify` не упоминает ни одно из %v (в Makefile есть %q)",
					place, required[makefileToken], makefileToken)
			}
		}
	}
}
