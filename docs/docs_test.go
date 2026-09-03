// Package docs guards against silent regressions in onboarding documentation:
// missing sections, a deleted glossary entry, or a broken cross-link would
// otherwise only be caught by a human re-reading the whole README.
package docs

import (
	"os"
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
		"AUDIT.md",
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
		"Gate rule",
	})
}

func TestDependencyKillWatchTracksSignals(t *testing.T) {
	// EXP-1: стратегическая ставка Track V обязана оставаться наблюдаемой;
	// удаление сценария/сигнала/действия или Stop-правила — явный акт.
	doc := readRepoFile(t, "DEPENDENCY-KILL-WATCH.md")
	feat := readRepoFile(t, "../FEATURES.cloud.md")

	assertContainsAll(t, doc, "docs/DEPENDENCY-KILL-WATCH.md", []string{
		"GitHub Agent HQ",
		"Claude Code / Codex",
		"«Верификация — не боль»",
		"Bundle никто не открывает, потому что он непонятен",
		"Сигнал раннего обнаружения",
		"Действие",
		"три подряд интервью дали «нам это не нужно»",
		"ноль внешних повторных запусков за 8 недель",
	})
	if !strings.Contains(feat, "Чего нет ни в codex, ни в ревизии 2") {
		t.Error("docs/DEPENDENCY-KILL-WATCH.md: не найден источник правил FEATURES.cloud.md §")
	}
}
