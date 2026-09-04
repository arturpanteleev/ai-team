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
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd: %v", err)
	}

	cfg := docsgen.Config{
		Root:        root,
		Output:      filepath.Join(root, *out),
		Title:       "ai-team",
		Version:     version(),
		BasePath:    *basePath,
		CleanOutput: true,
		Sources: []docsgen.SourcedPage{
			{Source: "README.md", Title: "Overview", Section: "Guide", Weight: 0, URL: "/"},
			{Source: "docs/ARCHITECTURE.md", Title: "Architecture", Section: "Reference", Weight: 0, URL: "/architecture/"},
			{Source: "docs/demo/README.md", Title: "Demo: gate → verify", Section: "Reference", Weight: 1, URL: "/demo/"},
			{Source: "CONTRIBUTING.md", Title: "Contributing", Section: "Community", Weight: 0, URL: "/contributing/"},
			{Source: "SECURITY.md", Title: "Security", Section: "Community", Weight: 1, URL: "/security/"},
			{Source: "CODE_OF_CONDUCT.md", Title: "Code of Conduct", Section: "Community", Weight: 2, URL: "/code-of-conduct/"},
			{Source: "CHANGELOG.md", Title: "Changelog", Section: "Project", Weight: 0, URL: "/changelog/"},
		},
	}

	if err := docsgen.Build(cfg); err != nil {
		log.Fatalf("build docs: %v", err)
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
