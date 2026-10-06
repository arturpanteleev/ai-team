// Command docsgen builds the static ai-team documentation site from the
// repository's Markdown sources.
//
// Usage: go run ./docsgen [--out <dir>]
//
// It reads the sources listed below relative to the repository root and
// writes a self-contained static site into the output directory (default
// docs/_site). The GitHub Pages workflow runs the same generator.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/docsgen"
)

func main() {
	out := flag.String("out", "docs/_site", "output directory for the generated site")
	basePath := flag.String("base-path", "", "site base path, e.g. /ai-team for a GitHub Pages project site")
	githubRepo := flag.String("github-repo", "arturpanteleev/ai-team", "owner/repo used to rewrite directory links to GitHub tree URLs")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd: %v", err)
	}

	cfg := docsgen.SiteConfig()
	cfg.Root = root
	cfg.Output = filepath.Join(root, *out)
	cfg.Version = version()
	cfg.BasePath = *basePath
	cfg.CleanOutput = true
	cfg.GitHubRepo = *githubRepo

	if err := docsgen.Build(cfg); err != nil {
		log.Fatalf("build docs: %v", err)
	}
	if err := docsgen.CheckLinksWithBase(cfg.Output, cfg.BasePath); err != nil {
		log.Fatalf("link check: %v", err)
	}
	fmt.Printf("docs site built to %s\n", cfg.Output)
}

// version returns the tag if HEAD is tagged, else "dev". It mirrors the
// Makefile's TAG computation for consistency with the built binary.
func version() string {
	out, err := gitDescribe()
	if err != nil {
		return "dev"
	}
	if out == "" {
		return "dev"
	}
	return out
}

func gitDescribe() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return runGit(root, "describe", "--tags", "--always", "--dirty")
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
