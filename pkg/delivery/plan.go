// Package delivery builds and executes controller-owned delivery plans.
package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion = 4
	DeletedDigest = "deleted"
	DeletedMode   = "deleted"
)

var (
	remotePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	scpRemotePattern = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?:[^:\s]+$`)
	branchPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	gitHashPattern   = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
	sha256Pattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// gitCommandArgs disables repository-configured hooks, fsmonitor executables,
// commit signing, and the external-helper transport for every controller-owned
// Git invocation. The explicit protocol setting overrides repository config.
func gitCommandArgs(args ...string) []string {
	configured := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "commit.gpgsign=false",
		"-c", "protocol.ext.allow=never",
	}
	return append(configured, args...)
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" || args[i] == "--config-env" {
			i++ // These global Git options consume one following argument.
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		return args[i]
	}
	return ""
}

func isGitNetworkOperation(args []string) bool {
	switch gitSubcommand(args) {
	case "push", "fetch", "ls-remote":
		return true
	default:
		return false
	}
}

// Plan is the complete, reviewable declaration of allowed delivery effects.
// Commands and shell fragments are deliberately not part of the schema.
type Plan struct {
	SchemaVersion           int                             `json:"schema_version"`
	Branch                  string                          `json:"branch"`
	BaseBranch              string                          `json:"base_branch"`
	Remote                  string                          `json:"remote"`
	RemoteURL               string                          `json:"remote_url"`
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
	if err := validateRemoteURL(p.RemoteURL); err != nil {
		return err
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
		if isControlPath(file) {
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

func validateRemoteURL(remoteURL string) error {
	if remoteURL == "" || strings.TrimSpace(remoteURL) != remoteURL || !utf8.ValidString(remoteURL) {
		return fmt.Errorf("delivery plan: remote_url обязателен и должен быть корректной строкой")
	}
	if strings.HasPrefix(remoteURL, "-") {
		return fmt.Errorf("delivery plan: remote_url не должен начинаться с option prefix")
	}
	for _, r := range remoteURL {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("delivery plan: remote_url содержит управляющие символы")
		}
	}
	if strings.ContainsAny(remoteURL, "?#") {
		return fmt.Errorf("delivery plan: remote_url с query или fragment запрещён")
	}
	if filepath.IsAbs(remoteURL) {
		// Check local paths before URI syntax: an absolute path can contain
		// "://" in a component (for example /tmp/team://remote.git). It is still
		// a filesystem destination, not a URI with an unsupported scheme.
		return nil
	}
	if !strings.Contains(remoteURL, "://") {
		if !scpRemotePattern.MatchString(remoteURL) {
			return fmt.Errorf("delivery plan: remote_url должен быть локальным путём или поддерживаемым SCP-style адресом [user@]host:path")
		}
		return nil
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" {
		return fmt.Errorf("delivery plan: remote_url имеет некорректный формат")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.User != nil {
			return fmt.Errorf("delivery plan: HTTP remote_url с embedded credentials запрещён; настройте Git credential helper")
		}
	case "ssh":
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return fmt.Errorf("delivery plan: SSH remote_url с embedded password запрещён; настройте SSH key или agent")
			}
		}
	default:
		return fmt.Errorf("delivery plan: remote_url protocol не поддерживается")
	}
	return nil
}

// singleRemoteURL accepts only the one effective push destination reported by
// `git remote get-url --push --all`. It deliberately does not return any URL
// when the configuration is ambiguous, so callers can fail without recording
// configured destinations in delivery evidence.
func singleRemoteURL(output string) (string, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", fmt.Errorf("push URL is not configured")
	}
	if strings.Contains(output, "\n") {
		return "", fmt.Errorf("multiple push URLs are configured")
	}
	if strings.TrimSpace(output) != output || strings.ContainsRune(output, '\r') {
		return "", fmt.Errorf("push URL output is malformed")
	}
	return output, nil
}

// rejectRemoteURLRewrites fails closed when Git could rewrite an approved
// effective URL again while push, fetch, or ls-remote receives it directly.
// `git config --null --list` includes system, global, repository, included,
// and environment-provided configuration used by the invoking Git process.
func rejectRemoteURLRewrites(configOutput, remoteURL string) error {
	if configOutput == "" {
		return nil
	}
	if !strings.HasSuffix(configOutput, "\x00") {
		return fmt.Errorf("delivery: Git URL rewrite configuration could not be safely parsed")
	}
	for _, record := range strings.Split(strings.TrimSuffix(configOutput, "\x00"), "\x00") {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return fmt.Errorf("delivery: Git URL rewrite configuration could not be safely parsed")
		}
		lowerKey := strings.ToLower(key)
		if !strings.HasPrefix(lowerKey, "url.") ||
			!(strings.HasSuffix(lowerKey, ".insteadof") || strings.HasSuffix(lowerKey, ".pushinsteadof")) {
			continue
		}
		// A newline in a rewrite value cannot be represented as an ordinary URL
		// prefix and makes the config record ambiguous. Reject it rather than
		// risk interpreting only part of the configured value.
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("delivery: Git URL rewrite configuration could not be safely parsed")
		}
		if strings.HasPrefix(remoteURL, value) {
			return fmt.Errorf("delivery: approved push URL matches a configured Git URL rewrite")
		}
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

// isControlPath запрещает плану целиться в служебные директории. Файл под
// ".git/" — это исполнение произвольного кода у каждого, кто сделает pull
// (хватает одного hooks/pre-commit), а ".ai-team/" — состояние самого прогона,
// которым агент мог бы переписать собственную историю и evidence.
//
// ".git" запрещён на любом уровне вложенности: вложенная рабочая копия —
// такая же control-директория, и hook из неё выполняется так же. ".ai-team"
// проверяется только в корне: глубже это обычная директория пользователя.
func isControlPath(file string) bool {
	for index, segment := range strings.Split(file, "/") {
		segment = controlSegment(segment)
		if segment == ".git" || segment == "git~1" {
			return true
		}
		if index == 0 && segment == ".ai-team" {
			return true
		}
	}
	return false
}

// controlSegment приводит компонент пути к тому имени, которое в итоге увидит
// файловая система. Сравнение «как записано» пропускало бы любую другую запись
// того же control path: APFS и NTFS сравнивают имена без учёта регистра
// (".GIT/hooks/pre-commit"), Windows отбрасывает хвостовые точки и пробелы
// (".git."), NTFS открывает саму директорию через alternate data stream
// (".git::$INDEX_ALLOCATION") и через 8.3-имя ("git~1"). Тот же набор
// вариантов закрывают core.protectHFS/protectNTFS в самом git.
func controlSegment(segment string) string {
	if index := strings.Index(segment, ":"); index >= 0 {
		segment = segment[:index]
	}
	return strings.ToLower(strings.TrimRight(segment, ". "))
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
