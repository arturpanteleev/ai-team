package scope

import "testing"

// MatchAny — предикат безопасности: в pkg/pipeline/guard.go он единственный,
// кто отделяет разрешённую агенту мутацию от запрещённой, а в
// pkg/redact/policy.go решает, попадёт ли файл под сканирование секретов.
// Поэтому ниже проверяются не только пути, которые обязаны совпасть, но —
// в первую очередь — те, которые совпасть не должны. Каждый негативный кейс
// подобран так, чтобы отвергала его ровно одна проверка реализации: тогда
// ослабление этой проверки гарантированно роняет тест.

func TestMatchAny(t *testing.T) {
	patterns := []string{"**/*_test.go", "tests/**", "**/*.spec.*"}
	tests := map[string]bool{
		"handler_test.go":          true,
		"pkg/api/handler_test.go":  true,
		"tests/fixture/input.json": true,
		"web/button.spec.tsx":      true,
		"pkg/api/handler.go":       false,
	}
	for value, want := range tests {
		if got := MatchAny(patterns, value); got != want {
			t.Errorf("MatchAny(%q) = %v, want %v", value, got, want)
		}
	}
}

// matchCase — один вход предиката. why описывает, какая именно проверка
// реализации обязана отвергнуть (или пропустить) этот вход.
type matchCase struct {
	name    string
	pattern string
	value   string
	want    bool
	why     string
}

func runMatchCases(t *testing.T, cases []matchCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchAny([]string{c.pattern}, c.value); got != c.want {
				t.Errorf("MatchAny([%q], %q) = %v, want %v\nпочему: %s",
					c.pattern, c.value, got, c.want, c.why)
			}
		})
	}
}

// TestMatchAnyEndAnchor. Без '$' точное совпадение вырождается в префиксное:
// паттерн начинает пропускать любой путь, у которого разрешённый путь —
// лишь начало. Это позволило бы агенту с allowed_paths `cmd/main.go`
// переписать `cmd/main.go.bak`, а агенту с `docs/*` — уйти вглубь дерева.
func TestMatchAnyEndAnchor(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "каталог глубже одного уровня",
			pattern: "docs/*",
			value:   "docs/a/b/c",
			want:    false,
			why:     "'*' не пересекает '/', и '$' запрещает совпадение по префиксу",
		},
		{
			name:    "суффикс за точным именем файла",
			pattern: "cmd/main.go",
			value:   "cmd/main.go.bak",
			want:    false,
			why:     "чистый литерал: отвергнуть вход способен только якорь '$'",
		},
		{
			name:    "расширение дописано после разрешённого",
			pattern: "pkg/scope/*.go",
			value:   "pkg/scope/path.go.evil",
			want:    false,
			why:     "'[^/]*' совпадает с 'path', '\\.go' — с '.go'; хвост '.evil' отсекает только '$'",
		},
		{
			name:    "глоб с ** и дописанным суффиксом",
			pattern: "**/*_test.go",
			value:   "pkg/api/handler_test.go.orig",
			want:    false,
			why:     "префикс '(?:.*/)?' и литерал совпадают целиком; хвост отсекает только '$'",
		},
		{
			name:    "перевод строки в конце имени",
			pattern: "docs/a.md",
			value:   "docs/a.md\n../evil",
			want:    false,
			why:     "'$' в Go — конец текста, а не строки; иначе имя с '\\n' протащило бы второй путь",
		},
	})
}

// TestMatchAnyStartAnchor. Без '^' паттерн совпадает с любым суффиксом пути,
// то есть разрешение на `docs/` автоматически распространяется на
// `<что угодно>/docs/`. Во всех кейсах ниже '$' и '[^/]' совпадению не мешают:
// отвергает вход ровно якорь начала.
func TestMatchAnyStartAnchor(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "разрешённый каталог вложен в чужой",
			pattern: "docs/*",
			value:   "evil/docs/a",
			want:    false,
			why:     "без '^' совпал бы суффикс 'docs/a'",
		},
		{
			name:    "разрешённый файл вложен в чужой каталог",
			pattern: "cmd/main.go",
			value:   "vendor/cmd/main.go",
			want:    false,
			why:     "без '^' совпал бы суффикс 'cmd/main.go'",
		},
		{
			name:    "перевод строки перед разрешённым путём",
			pattern: "docs/a.md",
			value:   "\ndocs/a.md",
			want:    false,
			why:     "'^' в Go — начало текста, а не строки: многострочное имя не пролезает",
		},
	})
}

// TestMatchAnySeparatorIsNotWildcard. '*' и '?' обязаны оставаться в пределах
// одного сегмента: если их расширить до '.' / '.*', разрешение на один
// уровень дерева молча станет разрешением на всё поддерево.
func TestMatchAnySeparatorIsNotWildcard(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "звезда в середине не проглатывает разделитель",
			pattern: "docs/*/x.md",
			value:   "docs/a/b/x.md",
			want:    false,
			why:     "оба якоря на месте и литералы совпадают: отвергает только '[^/]' внутри '*'",
		},
		{
			name:    "знак вопроса не проглатывает разделитель",
			pattern: "a?c",
			value:   "a/c",
			want:    false,
			why:     "длина совпадает, якоря не мешают: отвергает только '[^/]'",
		},
		{
			name:    "знак вопроса требует ровно один символ",
			pattern: "a?c",
			value:   "ac",
			want:    false,
			why:     "'[^/]' — обязательный символ, а не необязательный",
		},
		{
			name:    "одиночная звезда не поднимается на уровень выше",
			pattern: "*",
			value:   "pkg/api.go",
			want:    false,
			why:     "паттерн верхнего уровня не должен покрывать вложенные каталоги",
		},
	})
}

// TestMatchAnyMetacharactersAreLiteral. Паттерн — glob, а не регулярное
// выражение. Если литералы перестать экранировать через regexp.QuoteMeta,
// безобидные символы в имени файла превратятся в операторы: '.' начнёт
// совпадать с чем угодно, '|' разорвёт якоря, '[]' станет классом символов.
func TestMatchAnyMetacharactersAreLiteral(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "точка не любой символ",
			pattern: "cmd/main.go",
			value:   "cmd/mainXgo",
			want:    false,
			why:     "без QuoteMeta '.' совпала бы с 'X'",
		},
		{
			name:    "плюс не квантификатор",
			pattern: "a+b.go",
			value:   "ab.go",
			want:    false,
			why:     "без QuoteMeta 'a+' совпал бы с одной 'a', а '+' исчез бы из имени",
		},
		{
			name:    "скобки не класс символов",
			pattern: "[a-z].go",
			value:   "a.go",
			want:    false,
			why:     "без QuoteMeta '[a-z]' стал бы классом и совпал с любой буквой",
		},
		{
			name:    "вертикальная черта не альтернация",
			pattern: "docs/a|b.md",
			value:   "docs/a",
			want:    false,
			why:     "без QuoteMeta '|' разорвал бы выражение на '^docs/a' и 'b\\.md$' — якоря перестали бы работать",
		},
		{
			name:    "круглые скобки не группа",
			pattern: "docs/(a).md",
			value:   "docs/a.md",
			want:    false,
			why:     "без QuoteMeta скобки стали бы группой и исчезли из имени",
		},
		{
			name:    "литеральные метасимволы совпадают сами с собой",
			pattern: "[a-z].go",
			value:   "[a-z].go",
			want:    true,
			why:     "экранирование не должно ломать пути, которые действительно так называются",
		},
	})
}

// TestMatchAnyCaseSensitive. Repository-relative пути регистрозависимы;
// добавление флага (?i) сделало бы `DOCS/**` разрешением на `docs/**`.
func TestMatchAnyCaseSensitive(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "паттерн в верхнем регистре не покрывает нижний",
			pattern: "DOCS/*",
			value:   "docs/a.md",
			want:    false,
			why:     "сопоставление обязано оставаться регистрозависимым",
		},
		{
			name:    "расширение в верхнем регистре не покрывает нижний",
			pattern: "**/*.GO",
			value:   "pkg/api.go",
			want:    false,
			why:     "сопоставление обязано оставаться регистрозависимым",
		},
	})
}

// TestMatchAnyDoubleStarBoundary. '**' расширяется агрессивно, поэтому его
// префикс обязан оставаться литералом: `tests/**` — это разрешение на
// каталог tests, а не на всё, что с 'tests' начинается.
func TestMatchAnyDoubleStarBoundary(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "соседний каталог с общим префиксом",
			pattern: "tests/**",
			value:   "testsx/secret.go",
			want:    false,
			why:     "'/' после 'tests' — литерал, а не часть '**'",
		},
		{
			name:    "сам каталог без содержимого",
			pattern: "tests/**",
			value:   "tests",
			want:    false,
			why:     "'tests/**' покрывает содержимое каталога, а не запись о самом каталоге",
		},
		{
			name:    "содержимое каталога покрыто",
			pattern: "tests/**",
			value:   "tests/a/b/c.json",
			want:    true,
			why:     "'**' обязан пересекать разделители",
		},
		{
			name:    "** в начале не обязателен",
			pattern: "**/*_test.go",
			value:   "handler_test.go",
			want:    true,
			why:     "'(?:.*/)?' — необязательная группа: файл в корне тоже покрыт",
		},
	})
}

// TestMatchAnyPathEscape. Значение нормализуется до каноничного вида до
// сопоставления. Иначе путь `docs/../secret.go` физически лежит вне docs/,
// но текстуально совпадает с `docs/**` — предикат разрешил бы мутацию за
// пределами scope. Ср. Validate: паттерну выходить за workspace запрещено,
// значению — тем более.
func TestMatchAnyPathEscape(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "выход из разрешённого каталога через ..",
			pattern: "docs/**",
			value:   "docs/../secret.go",
			want:    false,
			why:     "путь резолвится в 'secret.go' — вне docs/",
		},
		{
			name:    "выход через .. с Windows-разделителем",
			pattern: "docs/**",
			value:   `docs\..\secret.go`,
			want:    false,
			why:     "обратные слэши нормализуются, и '..' обязан учитываться после нормализации",
		},
		{
			name:    "выход за пределы workspace",
			pattern: "**",
			value:   "../secret.go",
			want:    false,
			why:     "'**' покрывает всё внутри workspace, но не снаружи — отвергает только нормализация",
		},
		{
			name:    "абсолютный путь",
			pattern: "**",
			value:   "/etc/passwd",
			want:    false,
			why:     "'**' покрывает всё внутри workspace, но абсолютный путь repository-relative не является",
		},
		{
			name:    "пустое значение",
			pattern: "**",
			value:   "",
			want:    false,
			why:     "пустой путь не является repository-relative",
		},
		{
			name:    "одна точка",
			pattern: "**",
			value:   ".",
			want:    false,
			why:     "корень рабочего дерева — не файл, который можно менять",
		},
		{
			name:    ".. внутри пути, не выходящий наружу",
			pattern: "docs/**",
			value:   "docs/a/../b.md",
			want:    true,
			why:     "нормализация обязана резолвить путь, а не отвергать любой '..'",
		},
		{
			name:    "./ в начале значения",
			pattern: "docs/*",
			value:   "./docs/a.md",
			want:    true,
			why:     "'./' — синоним корня рабочего дерева",
		},
		{
			name:    "Windows-разделители в значении",
			pattern: "docs/*",
			value:   `docs\a.md`,
			want:    true,
			why:     "снапшот на Windows приносит обратные слэши",
		},
		{
			name:    "Windows-разделители в паттерне",
			pattern: `docs\*`,
			value:   "docs/a.md",
			want:    true,
			why:     "паттерн нормализуется тем же кодом, что и значение",
		},
	})
}

// TestMatchAnyNonASCII — регрессия на дефект, найденный при написании этих
// тестов: литерал собирался как string(pattern[i]), то есть каждый байт
// UTF-8 трактовался как отдельный code point. `docs/й.md` компилировался в
// `^docs/Ð¹\.md$` и переставал совпадать со своим же файлом — но начинал
// совпадать с чужим, чьё имя и есть эта мозаика. Для redaction.include это
// означало бы, что файл с кириллицей в имени молча не сканируется на секреты.
func TestMatchAnyNonASCII(t *testing.T) {
	runMatchCases(t, []matchCase{
		{
			name:    "паттерн совпадает со своим же файлом",
			pattern: "docs/й.md",
			value:   "docs/й.md",
			want:    true,
			why:     "литерал обязан собираться поруново, а не побайтово",
		},
		{
			name:    "длинное нелатинское имя",
			pattern: "docs/кириллица.md",
			value:   "docs/кириллица.md",
			want:    true,
			why:     "литерал обязан собираться поруново, а не побайтово",
		},
		{
			name:    "побайтовая мозаика не совпадает",
			pattern: "docs/й.md",
			value:   "docs/Ð¹.md",
			want:    false,
			why:     "именно этот путь совпадал при побайтовой сборке литерала",
		},
		{
			name:    "звезда покрывает нелатинское имя",
			pattern: "docs/*.md",
			value:   "docs/кириллица.md",
			want:    true,
			why:     "'[^/]*' работает на уровне рун",
		},
		{
			name:    "знак вопроса — одна руна, а не один байт",
			pattern: "docs/?.md",
			value:   "docs/й.md",
			want:    true,
			why:     "'[^/]' в Go-регулярке совпадает с руной целиком",
		},
		{
			name:    "знак вопроса не покрывает две руны",
			pattern: "docs/?.md",
			value:   "docs/йё.md",
			want:    false,
			why:     "'[^/]' — ровно одна руна",
		},
	})
}

// TestMatchAnyEmptyInputs. Пустой набор паттернов — это «не разрешено
// ничего»: агент без allowed_paths не имеет права менять ни одного файла.
// Пустой паттерн внутри набора не должен превращаться в «разрешено всё» или
// совпадать с чем-либо вообще.
func TestMatchAnyEmptyInputs(t *testing.T) {
	if MatchAny(nil, "pkg/api.go") {
		t.Error("nil-набор паттернов обязан отвергать любой путь")
	}
	if MatchAny([]string{}, "pkg/api.go") {
		t.Error("пустой набор паттернов обязан отвергать любой путь")
	}
	for _, value := range []string{"pkg/api.go", "", ".", "/"} {
		if MatchAny([]string{""}, value) {
			t.Errorf("пустой паттерн совпал с %q", value)
		}
	}
}

// TestMatchAnyRejectsUncompilablePattern. Паттерн, который не компилируется,
// обязан вести себя как «не совпало», а не как «совпало» и не как паника, —
// и не должен мешать остальным паттернам набора.
func TestMatchAnyRejectsUncompilablePattern(t *testing.T) {
	invalid := string([]byte{0xff, 'b', 'a', 'd'})
	if MatchAny([]string{invalid}, "ÿbad") {
		t.Error("паттерн с невалидным UTF-8 не должен совпадать с мозаикой из своих байтов")
	}
	if MatchAny([]string{invalid}, "pkg/api.go") {
		t.Error("некомпилируемый паттерн обязан вести себя как 'не совпало'")
	}
	if !MatchAny([]string{invalid, "pkg/*.go"}, "pkg/api.go") {
		t.Error("некомпилируемый паттерн не должен отменять валидные паттерны набора")
	}
	for _, escaping := range []string{"", "..", "../outside", "/absolute"} {
		if MatchAny([]string{escaping}, "pkg/api.go") {
			t.Errorf("паттерн %q выходит за workspace и не должен совпадать ни с чем", escaping)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, invalid := range []string{"", "../outside", "/absolute", `..\\outside`, ".", "./.."} {
		if err := Validate(invalid); err == nil {
			t.Errorf("Validate(%q) должна вернуть ошибку", invalid)
		}
	}
	// Невалидный UTF-8 раньше проходил валидацию и молча компилировался в
	// другой паттерн — теперь это явная ошибка конфигурации.
	if err := Validate(string([]byte{0xff, 'a'})); err == nil {
		t.Error("Validate должна отвергать паттерн с невалидным UTF-8")
	}
	for _, valid := range []string{"**/*_test.go", "tests/**", "docs/кириллица.md", "[a-z].go"} {
		if err := Validate(valid); err != nil {
			t.Fatalf("валидный scope %q: %v", valid, err)
		}
	}
}
