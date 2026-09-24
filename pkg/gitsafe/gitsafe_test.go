package gitsafe

import (
	"strings"
	"testing"
)

func TestArgsPrependsOverridesAndKeepsCommand(t *testing.T) {
	args := Args("-C", "/repo", "push", "-u", "origin", "ai-team/feat")
	tail := strings.Join(args[len(args)-6:], " ")
	if tail != "-C /repo push -u origin ai-team/feat" {
		t.Fatalf("исходная команда искажена: %v", args)
	}
	joined := strings.Join(args[:len(args)-6], " ")
	for _, expected := range []string{
		"-c core.hooksPath=/dev/null",
		"-c core.fsmonitor=",
		"-c protocol.ext.allow=never",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("override %q отсутствует: %v", expected, args)
		}
	}
}

func TestArgsDoesNotAliasCallerSlice(t *testing.T) {
	original := []string{"status", "--porcelain"}
	Args(original...)
	if original[0] != "status" || original[1] != "--porcelain" {
		t.Fatalf("аргументы вызывающего изменены: %v", original)
	}
}

func TestEnvDropsInheritedGitConfigEntries(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/tmp/evil",
		"HOME=/home/u",
	}
	env := Env(base)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "/tmp/evil") {
		t.Fatalf("унаследованный GIT_CONFIG_VALUE_0 не вычищен: %v", env)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "HOME=/home/u") {
		t.Fatalf("обычные переменные окружения потеряны: %v", env)
	}
	if strings.Count(joined, "GIT_CONFIG_COUNT=") != 1 {
		t.Fatalf("GIT_CONFIG_COUNT должен быть ровно один: %v", env)
	}
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=3") {
		t.Fatalf("GIT_CONFIG_COUNT не соответствует числу overrides: %v", env)
	}
	// Значения overrides должны быть именно нашими, а не унаследованными.
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=core.hooksPath") ||
		!strings.Contains(joined, "GIT_CONFIG_VALUE_0=/dev/null") {
		t.Fatalf("overrides не выставлены: %v", env)
	}
}
