package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/provenance"
)

// syntheticRun строит immutable run evidence с фиксированным содержимым.
func syntheticRun(t *testing.T, root string) {
	t.Helper()
	runJSON := `{
  "schema_version": 6,
  "run_id": "r-0001",
  "feature": "feat",
  "target_dir": "/tmp/x",
  "started_at": "2026-01-02T03:04:05Z",
  "config_evidence": "config.json",
  "config_sha256": "c0ffee00000000000000000000000000000000000000000000c0ffee00000000",
  "resolved_workflow_evidence": "workflow.json",
  "resolved_workflow_sha256": "b0ba0000000000000000000000000000000000000000000000b0ba0000000000",
  "controller": {
    "executable_sha256": "eec0aabb00000000000000000000000000000000000000000000000000000000",
    "go_version": "go1.26.5",
    "goos": "darwin",
    "goarch": "arm64"
  },
  "provenance": {
    "schema_version": 1,
    "run_id": "r-0001",
    "generated_at": "2026-01-02T03:04:04Z",
    "items": [
      {"kind": "runtime", "digest": {"type": "sha256", "value": "eec0aabb00000000000000000000000000000000000000000000000000000000"}},
      {"kind": "agent_definition", "name": "coder", "digest": {"type": "sha256", "value": "aaaa111100000000000000000000000000000000000000000000000000000000"}}
    ]
  }
}
`
	mustWrite(t, filepath.Join(root, "run.json"), []byte(runJSON))
	mustWrite(t, filepath.Join(root, "config.json"), []byte(`{"schema_version":1}`))
	mustWrite(t, filepath.Join(root, "workflow.json"), []byte(`{"schema_version":2}`))
	mustWrite(t, filepath.Join(root, "events.jsonl"), []byte("events:\n"))
	attempt := `{
  "schema_version": 6,
  "run_id": "r-0001",
  "attempt_id": "a-1",
  "stage": "coder",
  "stage_index": 2,
  "started_at": "2026-01-02T03:04:06Z",
  "finished_at": "2026-01-02T03:04:07Z",
  "status": "passed",
  "execution": "attempted",
  "decision": "approved",
  "outcome": "passed",
  "verdict": "APPROVED",
  "checks": [
    {"name": "go-test", "class": "unit", "adapter": "", "command": ["go", "test"], "policy": "required", "working_dir": "", "started_at": "2026-01-02T03:04:06Z", "finished_at": "2026-01-02T03:04:07Z", "duration_ns": 1000000, "exit_code": 0, "status": "passed", "reason": ""},
    {"name": "lint", "class": "lint", "adapter": "", "command": ["go", "vet"], "policy": "optional", "working_dir": "", "started_at": "2026-01-02T03:04:06Z", "finished_at": "2026-01-02T03:04:07Z", "duration_ns": 500000, "exit_code": 1, "status": "failed", "reason": "взвесь"}
  ],
  "mutation_changes": [
    {"path": "src/extra_test.go", "kind": "added", "class": "tests"}
  ]
}
`
	mustWrite(t, filepath.Join(root, "attempts", "a-1", "manifest.json"), []byte(attempt))
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func builtStatement(t *testing.T, root string) *Statement {
	t.Helper()
	decidedAt := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	statement, err := Build(Options{
		RunDir: root, RunID: "r-0001",
		FinishedAt: time.Date(2026, 1, 2, 3, 6, 0, 0, time.UTC),
		Outcome:    "passed",
		CandidateSubject: []Subject{{
			Name:   "candidate",
			Digest: map[string]string{"sha256": "aa11bb2200000000000000000000000000000000000000000000000000000000"},
		}},
		Approvals: []approval.PendingApproval{{
			ID: "ap-1", RunID: "r-0001", AttemptID: "a-1", FromStage: "coder", ToStage: "reviewer",
			Trigger: "graph_outcome:passed", SubjectHash: "feed0000", CandidateSHA256: "",
			RequiredRoles: []string{"operator"}, Quorum: "any", Status: approval.StatusResolved,
			Actions: []string{"approve", "reject"}, Targets: map[string]string{"approve": "reviewer"},
			Decisions:      []approval.Decision{{ActorID: "h-1", ActorRole: "operator", Action: "approve", SubjectHash: "feed0000", DecidedAt: decidedAt}},
			ResolvedAction: "approve",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func TestGoldenFixture(t *testing.T) {
	root := t.TempDir()
	syntheticRun(t, root)
	data, err := Serialize(builtStatement(t, root))
	if err != nil {
		t.Fatal(err)
	}
	// Золотой файл: фиксированный canonical JSON. Любое изменение структуры
	// predicate (поля/порядок/семантика) ломает тест — это compatibility pin.
	golden := `{
  "_type": "https://in-toto.io/Statement/v0.1",
  "subject": [
    {
      "name": "candidate",
      "digest": {
        "sha256": "aa11bb2200000000000000000000000000000000000000000000000000000000"
      }
    }
  ],
  "predicateType": "https://ai-team.dev/attestation/run/v1",
  "predicate": {
    "schema_version": 1,
    "run_id": "r-0001",
    "started_at": "2026-01-02T03:04:05Z",
    "finished_at": "2026-01-02T03:06:00Z",
    "outcome": "passed",
    "run": {
      "evidence_schema_version": 6,
      "config_evidence": "config.json",
      "config_sha256": "c0ffee00000000000000000000000000000000000000000000c0ffee00000000",
      "event_log_sha256": "f25581677fd3d7c353152b423f05c8d022aca4afc3bc2402fc847e92798a9afc",
      "controller_executable_sha256": "eec0aabb00000000000000000000000000000000000000000000000000000000",
      "attempt_count": 1
    },
    "spec": {
      "resolved_workflow_evidence": "workflow.json",
      "resolved_workflow_sha256": "b0ba0000000000000000000000000000000000000000000000b0ba0000000000"
    },
    "checks": [
      {
        "stage": "coder",
        "attempt_id": "a-1",
        "name": "go-test",
        "class": "unit",
        "policy": "required",
        "status": "passed",
        "exit_code": 0
      },
      {
        "stage": "coder",
        "attempt_id": "a-1",
        "name": "lint",
        "class": "lint",
        "policy": "optional",
        "status": "failed",
        "exit_code": 1,
        "reason": "взвесь"
      }
    ],
    "mutations": [
      {
        "attempt_id": "a-1",
        "stage": "coder",
        "path": "src/extra_test.go",
        "kind": "added",
        "class": "tests"
      }
    ],
    "approvals": [
      {
        "id": "ap-1",
        "trigger": "graph_outcome:passed",
        "from_stage": "coder",
        "to_stage": "reviewer",
        "subject_hash": "feed0000",
        "status": "resolved",
        "resolved_action": "approve",
        "decisions": [
          {
            "actor_role": "operator",
            "action": "approve",
            "subject_hash": "feed0000",
            "decided_at": "2026-01-02T03:05:00Z"
          }
        ]
      }
    ],
    "verdicts": [
      {
        "attempt_id": "a-1",
        "stage": "coder",
        "verdict": "APPROVED",
        "outcome": "passed",
        "status": "passed"
      }
    ],
    "provenance": {
      "schema_version": 1,
      "run_id": "r-0001",
      "generated_at": "2026-01-02T03:04:04Z",
      "items": [
        {
          "kind": "runtime",
          "digest": {
            "type": "sha256",
            "value": "eec0aabb00000000000000000000000000000000000000000000000000000000"
          }
        },
        {
          "kind": "agent_definition",
          "name": "coder",
          "digest": {
            "type": "sha256",
            "value": "aaaa111100000000000000000000000000000000000000000000000000000000"
          }
        }
      ]
    }
  }
}
`
	if string(data)+"\n" != golden {
		t.Fatalf("golden fixture diff\ngot:\n%s\nwant:\n%s", data, golden)
	}

	// Parse round-trip canonical JSON и digest детерминизм.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(parsed)
	if err != nil {
		t.Fatal(err)
	}
	digest2, err := Digest(builtStatement(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if digest != digest2 {
		t.Fatalf("digest не детерминирован: %s vs %s", digest, digest2)
	}
}

func TestParseRejectsUnknownFieldsAndVersion(t *testing.T) {
	root := t.TempDir()
	syntheticRun(t, root)
	data, err := Serialize(builtStatement(t, root))
	if err != nil {
		t.Fatal(err)
	}
	var structure map[string]any
	if err := json.Unmarshal(data, &structure); err != nil {
		t.Fatal(err)
	}
	predicate := structure["predicate"].(map[string]any)
	predicate["schema_version"] = 99.0
	tampered, err := json.MarshalIndent(structure, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(tampered); err == nil {
		t.Fatalf("неподдерживаемая schema_version должна отклоняться")
	}
	predicate["schema_version"] = float64(PredicateSchemaVersion)
	predicate["unknown_field"] = "x"
	tampered, err = json.MarshalIndent(structure, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(tampered); err == nil {
		t.Fatalf("unknown поле predicate должно отклоняться")
	}
}

func TestParseRejectsTrailingJSON(t *testing.T) {
	root := t.TempDir()
	syntheticRun(t, root)
	data, err := Serialize(builtStatement(t, root))
	if err != nil {
		t.Fatal(err)
	}
	garbage := append(append([]byte{}, data...), []byte(`{"extra":true}`)...)
	if _, err := Parse(garbage); err == nil {
		t.Fatalf("trailing JSON-документ должен отклоняться")
	}
}

func TestParseRejectsRunIDMismatch(t *testing.T) {
	root := t.TempDir()
	syntheticRun(t, root)
	base, err := Serialize(builtStatement(t, root))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(base)
	if err != nil {
		t.Fatalf("базовый Parse должен пройти: %v", err)
	}
	tampered := *parsed
	tampered.Predicate.Provenance = &provenance.Manifest{
		SchemaVersion: provenance.SchemaVersion,
		RunID:         "r-ДРУГОЙ",
	}
	data, err := Serialize(&tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err == nil {
		t.Fatalf("рассинхрон run_id predicate/provenance должен отклоняться")
	}
}
