package scope

import (
	"path"
	"strings"
)

// Классы repository-relative путей (V0-1: test-mutation provenance). Каждый
// attributed mutation одного этапа относится ровно к одному классу; класс
// "unknown" видим в evidence и отчёте и не может быть случайно улучшен до
// более привилегированного класса.
const (
	ClassSource    = "source"
	ClassTests     = "tests"
	ClassMeta      = "meta"
	ClassInfra     = "infra"
	ClassGenerated = "generated"
	ClassUnknown   = "unknown"
)

// testSegments — каталоги, считающиеся тестовым флотом. Зеркалит
// allowed_paths агента tester; "spec/specs" здесь намеренно НЕ намерены
// (каталоги спецификаций OpenSpec — документы, а не тесты).
var testSegments = map[string]bool{
	"testdata": true, "__tests__": true, "test": true, "tests": true,
	"e2e": true, "e2etest": true,
}

// generatedSegments — каталоги, содержимое которых генерируется инструментами
// и не является результатом ручного редактирования.
var generatedSegments = map[string]bool{
	"generated": true, "gen": true, "dist": true, "build": true, "out": true,
	"coverage": true, "node_modules": true, "vendor": true, ".cache": true, "tmp": true,
}

// infraSegments — каталоги CI/CD и оркестрации.
var infraSegments = map[string]bool{
	".github": true, ".gitlab": true, ".circleci": true, ".ci": true,
	"infra": true, "k8s": true, "deploy": true, ".buildkite": true,
}

// metaBases — манифесты зависимостей и edge-конфигурация проекта.
var metaBases = map[string]bool{
	"go.mod": true, "go.sum": true, "go.work": true, "go.work.sum": true,
	"package.json": true, "package-lock.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "bun.lockb": true,
	"Cargo.toml": true, "Cargo.lock": true,
	"requirements.txt": true, "pyproject.toml": true, "poetry.lock": true,
	"Pipfile": true, "Pipfile.lock": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true,
	".golangci.yml": true, ".golangci.yaml": true, ".pre-commit-config.yaml": true,
	".eslintrc": true, ".eslintrc.json": true, ".prettierrc": true,
	".nvmrc": true, ".tool-versions": true, ".mise.toml": true,
}

// sourceExts — расширения исходного кода (исключая тестовые пути).
var sourceExts = map[string]bool{
	".go": true, ".js": true, ".mjs": true, ".cjs": true, ".jsx": true,
	".ts": true, ".mts": true, ".cts": true, ".tsx": true,
	".py": true, ".java": true, ".rs": true, ".c": true, ".h": true,
	".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".cs": true,
	".rb": true, ".php": true, ".swift": true, ".kt": true, ".kts": true,
	".scala": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".sql": true, ".vue": true, ".svelte": true, ".dart": true, ".zig": true,
	".lua": true, ".ex": true, ".exs": true, ".erl": true, ".elm": true,
}

// isTestPath — принадлежит ли repository-relative путь тестовому флоту.
func isTestPath(value string) bool {
	base := path.Base(value)
	dir := path.Dir(value)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.go") {
		return true
	}
	if containsMark(base, ".test.") || containsMark(base, ".spec.") {
		return true
	}
	for _, segment := range pathSegments(dir) {
		if testSegments[segment] {
			return true
		}
		if strings.HasSuffix(segment, "_test") {
			return true
		}
	}
	return false
}

// containsMark — содержит ли строка разделённый точками маркер.
func containsMark(value, marker string) bool {
	for _, part := range strings.Split(value, ".") {
		if part == strings.Trim(marker, ".") {
			return true
		}
	}
	return false
}

func pathSegments(dir string) []string {
	if dir == "." || dir == "" {
		return nil
	}
	return strings.Split(dir, "/")
}

// ClassifyMutation относит один repository-relative mutation path к классу.
// Приоритеты: tests > generated > infra > meta > source > unknown — тестовые
// пути никогда не падают в generated/source, а метаданные зависимостей не
// маскируются под некод.
func ClassifyMutation(repoRelative string) string {
	value := path.Clean(strings.ReplaceAll(repoRelative, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	if value == "." || value == "" {
		return ClassUnknown
	}
	if isTestPath(value) {
		return ClassTests
	}
	base := path.Base(value)
	lowerBase := strings.ToLower(base)
	lower := strings.ToLower(value)
	dir := path.Dir(value)

	for _, segment := range pathSegments(dir) {
		if generatedSegments[strings.ToLower(segment)] {
			return ClassGenerated
		}
	}
	if strings.HasSuffix(lowerBase, ".pb.go") || strings.HasSuffix(lowerBase, ".pb.ts") ||
		strings.HasSuffix(lowerBase, ".min.js") || strings.HasSuffix(lowerBase, ".min.css") ||
		strings.HasSuffix(lowerBase, ".map") {
		return ClassGenerated
	}

	for _, segment := range pathSegments(dir) {
		if infraSegments[strings.ToLower(segment)] {
			return ClassInfra
		}
	}
	if base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") ||
		strings.HasSuffix(lowerBase, ".dockerfile") || base == "Jenkinsfile" ||
		base == "Makefile" || strings.HasSuffix(lowerBase, ".mk") ||
		base == ".gitlab-ci.yml" || base == ".gitlab-ci.yaml" || base == ".drone.yml" {
		return ClassInfra
	}

	if metaBases[base] || metaBases[lowerBase] {
		return ClassMeta
	}

	if strings.HasSuffix(lower, "/go.mod") || strings.HasSuffix(lower, "/go.sum") {
		return ClassMeta
	}

	extension := path.Ext(base)
	if sourceExts[strings.ToLower(extension)] {
		return ClassSource
	}

	return ClassUnknown
}
