// Package delivery builds and executes controller-owned delivery plans.
package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion = 3
	DeletedDigest = "deleted"
	DeletedMode   = "deleted"
)

var (
	remotePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	branchPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	gitHashPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
	sha256Pattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Plan is the complete, reviewable declaration of allowed delivery effects.
// Commands and shell fragments are deliberately not part of the schema.
type Plan struct {
	SchemaVersion           int                             `json:"schema_version"`
	Branch                  string                          `json:"branch"`
	BaseBranch              string                          `json:"base_branch"`
	Remote                  string                          `json:"remote"`
	Files                   []string                        `json:"files"`
	FileDigests             map[string]string               `json:"file_digests"`
	FileModes               map[string]string               `json:"file_modes"`
	BaselineHead            string                          `json:"baseline_head"`
	SourceRunID             string                          `json:"source_run_id"`
	VerifiedWorkspaceDigest string                          `json:"verified_workspace_digest"`
	CheckEvidenceDigest     string                          `json:"check_evidence_digest"`
	Preconditions           map[string]PreconditionEvidence `json:"preconditions"`
	CommitMessage           string                          `json:"commit_message"`
	PRTitle                 string                          `json:"pr_title"`
	PRBody                  string                          `json:"pr_body"`
}

type PreconditionEvidence struct {
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Verdict string `json:"verdict"`
}

func Parse(data []byte) (Plan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("delivery plan JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Plan{}, fmt.Errorf("delivery plan JSON: trailing value")
	} else if err != io.EOF {
		return Plan{}, fmt.Errorf("delivery plan JSON: trailing data: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p Plan) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("delivery plan: schema_version %d не поддерживается", p.SchemaVersion)
	}
	if !validBranch(p.Branch) {
		return fmt.Errorf("delivery plan: невалидная branch %q", p.Branch)
	}
	if !validBranch(p.BaseBranch) {
		return fmt.Errorf("delivery plan: невалидная base_branch %q", p.BaseBranch)
	}
	if p.Branch == p.BaseBranch || p.Branch == "main" || p.Branch == "master" {
		return fmt.Errorf("delivery plan: push в protected branch %q запрещён", p.Branch)
	}
	if !remotePattern.MatchString(p.Remote) {
		return fmt.Errorf("delivery plan: невалидный remote %q", p.Remote)
	}
	if len(p.Files) == 0 {
		return fmt.Errorf("delivery plan: files не может быть пустым")
	}
	if len(p.Files) > 1000 {
		return fmt.Errorf("delivery plan: слишком много files (%d > 1000)", len(p.Files))
	}
	seen := make(map[string]bool, len(p.Files))
	for _, file := range p.Files {
		if file == "" || strings.Contains(file, "\\") || path.IsAbs(file) || path.Clean(file) != file ||
			file == "." || file == ".." || strings.HasPrefix(file, "../") {
			return fmt.Errorf("delivery plan: file %q должен быть нормализованным workspace-relative путём", file)
		}
		if file == ".git" || strings.HasPrefix(file, ".git/") || file == ".ai-team" || strings.HasPrefix(file, ".ai-team/") {
			return fmt.Errorf("delivery plan: control path %q запрещён", file)
		}
		if seen[file] {
			return fmt.Errorf("delivery plan: file %q дублируется", file)
		}
		seen[file] = true
		digest, exists := p.FileDigests[file]
		if !exists || digest != DeletedDigest && !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("delivery plan: file_digests[%q] должен быть sha256 или %q", file, DeletedDigest)
		}
		mode, modeExists := p.FileModes[file]
		if !modeExists || mode != DeletedMode && mode != "100644" && mode != "100755" {
			return fmt.Errorf("delivery plan: file_modes[%q] должен быть 100644, 100755 или %q", file, DeletedMode)
		}
		if (digest == DeletedDigest) != (mode == DeletedMode) {
			return fmt.Errorf("delivery plan: deletion digest/mode для %q должны совпадать", file)
		}
	}
	if len(p.FileDigests) != len(p.Files) || len(p.FileModes) != len(p.Files) {
		return fmt.Errorf("delivery plan: file_digests/file_modes должны точно соответствовать files")
	}
	if !gitHashPattern.MatchString(p.BaselineHead) {
		return fmt.Errorf("delivery plan: baseline_head должен быть git object id")
	}
	if !runIDPattern.MatchString(p.SourceRunID) {
		return fmt.Errorf("delivery plan: source_run_id обязателен и невалиден")
	}
	if !sha256Pattern.MatchString(p.VerifiedWorkspaceDigest) {
		return fmt.Errorf("delivery plan: verified_workspace_digest должен быть sha256")
	}
	if !sha256Pattern.MatchString(p.CheckEvidenceDigest) {
		return fmt.Errorf("delivery plan: check_evidence_digest должен быть sha256")
	}
	if len(p.Preconditions) == 0 {
		return fmt.Errorf("delivery plan: preconditions evidence обязателен")
	}
	for name, evidence := range p.Preconditions {
		if name == "" || strings.TrimSpace(name) != name || evidence.Type != "file" || evidence.Size <= 0 ||
			!sha256Pattern.MatchString(evidence.SHA256) || evidence.Verdict == "" {
			return fmt.Errorf("delivery plan: precondition %q имеет невалидное evidence", name)
		}
	}
	if err := validateOneLine("commit_message", p.CommitMessage, 120); err != nil {
		return err
	}
	lowerCommit := strings.ToLower(p.CommitMessage)
	for _, forbidden := range []string{"generated by", "ai-authored", "co-authored-by"} {
		if strings.Contains(lowerCommit, forbidden) {
			return fmt.Errorf("delivery plan: commit_message содержит запрещённую атрибуцию %q", forbidden)
		}
	}
	if err := validateOneLine("pr_title", p.PRTitle, 200); err != nil {
		return err
	}
	if strings.TrimSpace(p.PRBody) == "" || utf8.RuneCountInString(p.PRBody) > 700 {
		return fmt.Errorf("delivery plan: pr_body обязателен и не должен превышать 700 символов")
	}
	return nil
}

func (p Plan) CanonicalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	canonical := p
	canonical.Files = append([]string(nil), p.Files...)
	sort.Strings(canonical.Files)
	return json.MarshalIndent(canonical, "", "  ")
}

func (p Plan) Hash() (string, error) {
	data, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func WritePlan(filePath string, plan Plan) error {
	data, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".delivery-plan-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func validBranch(value string) bool {
	return branchPattern.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.Contains(value, "//") && !strings.HasSuffix(value, "/") &&
		!strings.HasSuffix(value, ".") && !strings.HasSuffix(value, ".lock") &&
		!strings.ContainsAny(value, "~^:?*[\\ ")
}

func validateOneLine(field, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("delivery plan: %s обязателен, должен быть одной строкой и не длиннее %d символов", field, maxRunes)
	}
	return nil
}
