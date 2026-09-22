// Package docs guards against silent regressions in onboarding documentation:
// missing sections, a deleted glossary entry, or a broken cross-link would
// otherwise only be caught by a human re-reading the whole README.
package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
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

// cliCommandRe якорится на начало фрагмента: команда считается
// задокументированной, только если ячейка (или её вариант после «/») с неё
// НАЧИНАЕТСЯ. Без якоря упоминание `ai-team gc` в середине описания чужой
// строки закрывало бы требование, и удалённая строка проходила бы незамеченной.
//
// Имя команды — [a-z] и далее буквы, цифры, «-» и «_»: диспетчер цифры
// допускает, и запрет на них делал бы тест неисправимо красным для команды
// вроде `gc2`, описанной везде корректно.
var cliCommandRe = regexp.MustCompile("^\\s*`ai-team ([a-z][a-z0-9_-]*)")

const mainSourcePath = "../cmd/ai-team/main.go"

// parseMainSource разбирает cmd/ai-team/main.go один раз: и диспетчер, и текст
// справки живут в этом файле, и обоим сторожам нужен один и тот же AST.
func parseMainSource(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, mainSourcePath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("разбор %s: %v", mainSourcePath, err)
	}
	return fileSet, file
}

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
	fileSet, file := parseMainSource(t)

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
		t.Fatalf("%s: не найден switch по os.Args[1]", mainSourcePath)
	case 1:
	default:
		lines := make([]int, 0, len(dispatchers))
		for _, candidate := range dispatchers {
			lines = append(lines, fileSet.Position(candidate.Pos()).Line)
		}
		t.Fatalf("%s: найдено несколько switch по os.Args[1] (строки %v) — неясно, какой из них диспетчер", mainSourcePath, lines)
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
					mainSourcePath, fileSet.Position(expression.Pos()).Line, types.ExprString(expression))
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				t.Fatalf("%s: не разобрать литерал %s: %v", mainSourcePath, literal.Value, unquoteErr)
			}
			if dispatcherAliases[value] {
				continue
			}
			commands[value] = true
		}
	}
	if len(commands) == 0 {
		t.Fatalf("%s: не удалось извлечь ни одной команды из диспетчера", mainSourcePath)
	}
	return commands
}

// readmeCLICommands собирает команды из первой колонки строк раздела
// «CLI-справочник», начинающихся с «|». Прозаические упоминания
// (`ai-team run --resume ...` ниже по тексту) не считаются документированием
// команды в справочнике.
//
// Что именно считается строкой таблицы: любая строка секции, начинающаяся с
// «|», — разметка не отслеживается, поэтому такая же строка внутри fenced-блока
// ``` в этой секции тоже была бы прочитана как строка таблицы. Полноценный
// Markdown-разбор сюда не заводим: это тот же класс задач, что и #139.
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
		// Одна строка таблицы может законно документировать пару команд через
		// «/» (`ai-team version` / `ai-team help`), поэтому ячейка режется на
		// варианты, и каждый обязан НАЧИНАТЬСЯ с имени команды.
		//
		// Остаток исходной дыры: лишний вариант после «/» в ЧУЖОЙ ячейке
		// закроет требование, и строку про команду можно будет удалить
		// незаметно. Дыра существует только в ГОЛОЙ форме — вариант обязан
		// сам начинаться с имени команды:
		//
		//   | `ai-team version` / `ai-team help` / `ai-team gc` | … |   не ловим
		//   | `ai-team version` / `ai-team help` / см. также `ai-team gc` | … |
		//                                                                ловим,
		//   потому что якорь отбрасывает вариант, начинающийся с прозы.
		//
		// Отличить документирование от ссылки текстом нельзя — нужен разбор
		// таблицы (#139).
		for _, alternative := range strings.Split(cells[0], "/") {
			if match := cliCommandRe.FindStringSubmatch(alternative); match != nil {
				commands[match[1]] = true
			}
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

// usageCommandRe — строка справки, НАЧИНАЮЩАЯСЯ с имени команды.
//
// Что якорь даёт и чего не даёт: он отсекает упоминания в середине строки
// («…продолжите: ai-team run --resume …»), но НЕ отличает блок списка команд
// от примера, который сам начинается с новой строки, — строка вида
// «  ai-team gc --dry-run» в разделе флагов засчиталась бы как документирование
// команды. Разбор справки на секции сюда не заводим: это тот же класс задач,
// что и #139.
//
// Имя команды — [a-z] и далее буквы, цифры, «-» и «_»: диспетчер цифры
// допускает, и запрет на них делал бы тест неисправимо красным для команды
// вроде `gc2`, описанной везде корректно.
var usageCommandRe = regexp.MustCompile(`(?m)^\s*ai-team ([a-z][a-z0-9_-]*)`)

// isFmtPrintCall — вызов вида fmt.Print*/fmt.Fprint*; только его аргументы и
// попадают в вывод справки.
func isFmtPrintCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "fmt" {
		return false
	}
	return strings.HasPrefix(selector.Sel.Name, "Print") || strings.HasPrefix(selector.Sel.Name, "Fprint")
}

// usageCommands собирает строковые литералы, ПЕРЕДАННЫЕ АРГУМЕНТОМ в вызовы
// fmt.Print/Printf/Println и их F-варианты внутри printUsage. Это критерий
// синтаксический, а не «попало в вывод»: писатель у Fprint* не проверяется
// (подойдёт любой, включая io.Discard), и вызов под условием — например в
// ветке за env-флагом — засчитывается наравне с безусловным. То есть литерал,
// который пользователь в `ai-team help` не увидит, может быть засчитан.
//
// Текст берётся из AST, а не из запуска бинарника, поэтому сторож не зависит
// от сборки. Цена — он не исполняет код, и гарантии по направлениям РАЗНЫЕ:
//
//   - «команда есть в диспетчере, но отсутствует в справке» — ловится надёжно:
//     чтобы пройти, имя команды обязано быть в литерале-аргументе fmt.Print*;
//   - «справка описывает команду, которой нет в диспетчере» — ловится только
//     когда строка справки является голым литералом. Конкатенация
//     («"  ai-team frobnicate" + "   Фробницировать"»), Sprintf или сборка
//     строки в рантайме делают сторож слепым: справка рекламирует
//     несуществующую команду, а тест молчит.
//
// Второе направление достроят вместе с остальными парсерными сторожами (#139).
func usageCommands(t *testing.T) map[string]bool {
	t.Helper()
	_, file := parseMainSource(t)

	var usage strings.Builder
	found := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != "printUsage" {
			continue
		}
		found = true
		ast.Inspect(function, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isFmtPrintCall(call) {
				return true
			}
			for _, argument := range call.Args {
				literal, ok := argument.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("%s: не разобрать литерал справки: %v", mainSourcePath, err)
				}
				usage.WriteString(value)
				usage.WriteString("\n")
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s: не найдена функция printUsage", mainSourcePath)
	}

	commands := map[string]bool{}
	for _, match := range usageCommandRe.FindAllStringSubmatch(usage.String(), -1) {
		commands[match[1]] = true
	}
	if len(commands) == 0 {
		t.Fatalf("%s: в тексте printUsage не найдено ни одной команды", mainSourcePath)
	}
	return commands
}

// TestUsageTextMatchesDispatcher — справка это третий справочник команд рядом с
// README и диспетчером, и он уже успел разойтись в другую сторону: `ci-import`
// в printUsage не было. Сверяем с тем же устойчивым источником — AST
// диспетчера, — и в обе стороны, как и README.
func TestUsageTextMatchesDispatcher(t *testing.T) {
	dispatcher := dispatcherCommands(t)
	documented := usageCommands(t)

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
		t.Errorf("printUsage в cmd/ai-team/main.go: команды есть в диспетчере, но не описаны в справке: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("printUsage в cmd/ai-team/main.go: справка описывает команды, которых нет в диспетчере: %v", extra)
	}
}
