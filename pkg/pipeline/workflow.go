package pipeline

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/ui"
)

var stdinReader = bufio.NewReader(os.Stdin)

func hasGitChanges(dir string) bool {
	cmd := exec.Command("git", "diff", "--quiet")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return true
	}
	cmd2 := exec.Command("git", "status", "--porcelain")
	cmd2.Dir = dir
	out, err := cmd2.Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

func hasGitDir(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

func readVerdictFromDir(artifactRoot, feature, agentName string) string {
	path := agentOutputPath(artifactRoot, feature, agentName)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if strings.Contains(content, "**Verdict:** APPROVED") {
		return "APPROVED"
	}
	if strings.Contains(content, "**Verdict:** CHANGES_REQUESTED") {
		return "CHANGES_REQUESTED"
	}
	if strings.Contains(content, "**Verdict:** REJECTED") {
		return "REJECTED"
	}
	return ""
}

func agentOutputPath(artifactRoot, feature, agentName string) string {
	return strings.NewReplacer(
		"${feature}", feature,
		"{agent}", agentName,
	).Replace(artifactRoot + "/" + agentName + "/output.md")
}

func promptContinue(agent, nextAgent string) string {
	fmt.Printf("\n%s %s -> %s %s\n",
		ui.Colorize("⏸", ui.ColorCyan),
		ui.Colorize(agent, ui.ColorYellow),
		ui.Colorize(nextAgent, ui.ColorGreen),
		ui.Colorize("[Y/n/diff/summary]", ui.ColorBold),
	)
	fmt.Print("> ")
	text, err := stdinReader.ReadString('\n')
	if err != nil {
		return "y"
	}
	return strings.TrimSpace(strings.ToLower(text))
}

func promptRetry(agent string, retry, maxRetries int) string {
	fmt.Printf("\n%s %s: retry %d/%d %s\n",
		ui.Colorize("⟳", ui.ColorYellow),
		ui.Colorize(agent, ui.ColorYellow),
		retry+1, maxRetries,
		ui.Colorize("[Y/n/diff]", ui.ColorBold),
	)
	fmt.Print("> ")
	text, err := stdinReader.ReadString('\n')
	if err != nil {
		return "y"
	}
	return strings.TrimSpace(strings.ToLower(text))
}

func isTerminalStdin() bool {
	stat, _ := os.Stdin.Stat()
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func isCoderLike(name string) bool {
	return strings.Contains(strings.ToLower(name), "coder")
}

func isReviewerLike(name string) bool {
	return strings.Contains(strings.ToLower(name), "review")
}

func findAgentIndex(names []string, target string) int {
	for i, n := range names {
		if n == target {
			return i
		}
		if strings.Contains(strings.ToLower(n), strings.ToLower(target)) {
			return i
		}
	}
	return -1
}

func readStageSummary(artifactRoot, feature, agentName string) string {
	path := filepath.Join(artifactRoot, ".stage-summary", agentName+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "(summary не найден)"
	}
	s := string(data)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
