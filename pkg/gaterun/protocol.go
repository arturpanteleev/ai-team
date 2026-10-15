// Package gaterun runs gates that speak the JSON gate protocol.
//
// The package is split in three independent parts so a second launch adapter
// (the Docker gate, S08) can reuse the protocol without the host process
// machinery:
//
//   - protocol.go: the input/result JSON documents and their strict validation;
//   - launcher.go: how bytes get to and from a program (Launcher interface and
//     the host adapter that runs an argv without a shell);
//   - runner.go: classification of the outcome into a workflow.GateAttempt,
//     redaction, evidence storage and idempotence by attempt ID.
//
// The runner never decides the next stage and never touches the pipeline: the
// scheduler calls Runner.Run and records the returned attempt.
package gaterun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Protocol is the value of the "protocol" field of both documents.
const Protocol = "ai-team.gate/v1"

// Limits of the result document. They bound what a program can make the
// controller store, independent of the stdout limit.
const (
	MaxReasonBytes    = 2000
	MaxEvidenceRefs   = 20
	MaxEvidenceBytes  = 1 << 20
	maxEvidencePathLn = 200
)

// GateInfo identifies the pinned definition and the stage-unique instance.
type GateInfo struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Name    string `json:"name"`
}

// ArtifactInfo describes the checked version of the stage result.
type ArtifactInfo struct {
	// Type is md, link or approve.
	Type string `json:"type"`
	// SHA256 is the hash of the immutable result version.
	SHA256 string `json:"sha256"`
	// Path is the readable file with the result (md results). Empty for
	// results that have no file.
	Path string `json:"path,omitempty"`
	// LinkKind and URL describe link results.
	LinkKind string `json:"link_kind,omitempty"`
	URL      string `json:"url,omitempty"`
}

// PathsInfo lists the paths the program may use. A launch adapter rewrites
// them to what the program sees (for example container paths).
type PathsInfo struct {
	RepoRoot string `json:"repo_root"`
	// EvidenceDir is the only directory the program should write to; result
	// evidence paths are relative to it.
	EvidenceDir string `json:"evidence_dir"`
}

// LimitsInfo tells the program the limits it runs under.
type LimitsInfo struct {
	TimeoutSeconds int   `json:"timeout_seconds"`
	MaxOutputBytes int64 `json:"max_output_bytes"`
}

// Input is the document written to the program's stdin.
type Input struct {
	Protocol  string       `json:"protocol"`
	TaskID    string       `json:"task_id"`
	Stage     string       `json:"stage"`
	Visit     int          `json:"visit"`
	AttemptID string       `json:"attempt_id"`
	Gate      GateInfo     `json:"gate"`
	Artifact  ArtifactInfo `json:"artifact"`
	Paths     PathsInfo    `json:"paths"`
	Limits    LimitsInfo   `json:"limits"`
}

// EvidenceRef is one evidence file named by the program. Path is relative to
// Input.Paths.EvidenceDir.
type EvidenceRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// Result is the single JSON document the program prints to stdout.
type Result struct {
	Protocol string        `json:"protocol"`
	Verdict  string        `json:"verdict"`
	Reason   string        `json:"reason,omitempty"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	kindPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	attemptIDRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ValidAttemptID reports whether id is safe to use as a directory name.
func ValidAttemptID(id string) bool {
	return attemptIDRe.MatchString(id) && !strings.Contains(id, "..")
}

// Validate checks the input document.
func (in Input) Validate() error {
	switch {
	case in.Protocol != Protocol:
		return fmt.Errorf("protocol must be %q", Protocol)
	case in.TaskID == "" || in.Stage == "" || in.Visit <= 0:
		return fmt.Errorf("task_id, stage and visit are required")
	case !ValidAttemptID(in.AttemptID):
		return fmt.Errorf("attempt_id %q is not a safe identifier", in.AttemptID)
	case in.Gate.ID == "" || in.Gate.Version <= 0 || in.Gate.Name == "":
		return fmt.Errorf("gate id, version and name are required")
	case !sha256Pattern.MatchString(in.Artifact.SHA256):
		return fmt.Errorf("artifact.sha256 must be a lowercase hex SHA-256")
	case in.Artifact.Type == "":
		return fmt.Errorf("artifact.type is required")
	}
	return nil
}

// ParseResult decodes and validates the program's stdout. Anything but exactly
// one well-formed document with a known verdict is an error, so malformed
// output can never be read as pass. A fail needs a reason.
func ParseResult(stdout []byte, limit int64) (Result, error) {
	var result Result
	if err := strictjson.Unmarshal(stdout, limit, &result); err != nil {
		return Result{}, err
	}
	if err := rejectAmbiguousKeys(stdout, resultShape); err != nil {
		return Result{}, fmt.Errorf("ambiguous document: %w", err)
	}
	if result.Protocol != Protocol {
		return Result{}, fmt.Errorf("result.protocol must be %q", Protocol)
	}
	if !workflow.GateVerdict(result.Verdict).Valid() {
		return Result{}, fmt.Errorf("result.verdict must be pass, fail or not_applicable")
	}
	if !utf8.ValidString(result.Reason) || len(result.Reason) > MaxReasonBytes {
		return Result{}, fmt.Errorf("result.reason must be valid UTF-8 of at most %d bytes", MaxReasonBytes)
	}
	if result.Verdict == string(workflow.GateVerdictFail) && strings.TrimSpace(result.Reason) == "" {
		return Result{}, fmt.Errorf("result.reason is required for verdict fail")
	}
	if len(result.Evidence) > MaxEvidenceRefs {
		return Result{}, fmt.Errorf("result.evidence has more than %d entries", MaxEvidenceRefs)
	}
	seen := map[string]bool{}
	for i, ref := range result.Evidence {
		if !kindPattern.MatchString(ref.Kind) {
			return Result{}, fmt.Errorf("result.evidence[%d].kind must match %s", i, kindPattern)
		}
		if err := validEvidencePath(ref.Path); err != nil {
			return Result{}, fmt.Errorf("result.evidence[%d].path: %w", i, err)
		}
		if seen[ref.Path] {
			return Result{}, fmt.Errorf("result.evidence[%d].path %q is repeated", i, ref.Path)
		}
		seen[ref.Path] = true
		if ref.SHA256 != "" && !sha256Pattern.MatchString(ref.SHA256) {
			return Result{}, fmt.Errorf("result.evidence[%d].sha256 must be a lowercase hex SHA-256", i)
		}
	}
	return result, nil
}

func validEvidencePath(p string) error {
	if p == "" || len(p) > maxEvidencePathLn {
		return fmt.Errorf("must be a non-empty relative path of at most %d bytes", maxEvidencePathLn)
	}
	if strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		return fmt.Errorf("must be a relative slash-separated path")
	}
	if path.Clean(p) != p {
		return fmt.Errorf("must be clean and stay inside the evidence directory")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("must be clean and stay inside the evidence directory")
		}
	}
	return nil
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
