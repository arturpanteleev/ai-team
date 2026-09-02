package e2etest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gateJUnitGateYAML = `schema_version: 1
diff_policy:
  test_modify: off
checks:
  - name: pytest-unit
    class: unit
    adapter: junit-xml
    report_file: report.xml
    command: ["sh", "-c", "cat reports/pytest-pass.xml > report.xml"]
    policy: required
`

const gateJUnitPassXML = `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" tests="2" failures="0" errors="0" skipped="0">
    <testcase classname="test_app" name="test_add" time="0.001"/>
    <testcase classname="test_app" name="test_parse" time="0.002"/>
  </testsuite>
</testsuites>
`

const gateJUnitFailXML = `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" tests="2" failures="1" errors="0" skipped="0">
    <testcase classname="test_app" name="test_add" time="0.001"/>
    <testcase classname="test_app" name="test_parse" time="0.002">
      <failure message="AssertionError: parse failed" type="AssertionError"/>
    </testcase>
  </testsuite>
</testsuites>
`

// TestE2E_GateJUnitAdapterNonGoRepo доказывает, что adapter junit-xml снимает
// ограничение «только Go» (V0-6): в не-Go репозитории (Python + pytest-отчёт)
// gate проходит/падает по JUnit отчёту и валидирует evidence digest.
func TestE2E_GateJUnitAdapterNonGoRepo(t *testing.T) {
	bin := buildBinary(t)
	repo := t.TempDir()
	gateGit(t, repo, "init", "-q")
	gateGit(t, repo, "config", "user.email", "e2e@test")
	gateGit(t, repo, "config", "user.name", "E2E Gate")
	gateWrite(t, repo, map[string]string{
		"app.py":                  "def add(a, b):\n    return a + b\n",
		"tests/test_app.py":       "from app import add\n\ndef test_add():\n    assert add(1, 2) == 3\n",
		"gate.yaml":               gateJUnitGateYAML,
		"reports/pytest-pass.xml": gateJUnitPassXML,
	})
	gateGit(t, repo, "add", "-A")
	gateGit(t, repo, "commit", "-q", "-m", "base")

	// PASS: junit-xml adapter верифицирует отчёт, gate выходит 0.
	outDir := filepath.Join(t.TempDir(), "gate-pass")
	code, out := runAI(t, bin, t.TempDir(), nil, "gate", "--target", repo, "--out", outDir)
	if code != 0 {
		t.Fatalf("pass gate: exit=%d, out:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Fatalf("PASS-строка не найдена:\n%s", out)
	}
	// Демо-цепочка gate → bundle → verify: verify обязан принять gate bundle
	// и перевычислить детерминированный bundle_sha256 (V0-7).
	vcode, vout := runAI(t, bin, t.TempDir(), nil, "verify", outDir)
	if vcode != 0 {
		t.Fatalf("verify gate bundle: exit=%d, out:\n%s", vcode, vout)
	}
	if !strings.Contains(vout, "bundle_sha256") {
		t.Fatalf("verify не напечатал digest:\n%s", vout)
	}
	checkData, err := os.ReadFile(filepath.Join(outDir, "checks", "001-pytest-unit.json"))
	if err != nil {
		t.Fatalf("check evidence: %v", err)
	}
	var check map[string]any
	if err := json.Unmarshal(checkData, &check); err != nil {
		t.Fatal(err)
	}
	if check["status"] != "passed" || check["discovered_tests"].(float64) != 2 || check["passed_tests"].(float64) != 2 {
		t.Fatalf("check evidence = %s", checkData)
	}
	if sha, ok := check["structured_output_sha256"].(string); !ok || sha == "" {
		t.Fatalf("report digest обязан быть в evidence: %s", checkData)
	}

	// FAIL: отчёт с failure — required check падает даже при exit code sh = 0.
	gateWrite(t, repo, map[string]string{"reports/pytest-pass.xml": gateJUnitFailXML})
	gateGit(t, repo, "add", "-A")
	gateGit(t, repo, "commit", "-q", "-m", "introduce failing test report")
	code, out = runAI(t, bin, t.TempDir(), nil, "gate", "--target", repo, "--out", filepath.Join(t.TempDir(), "gate-fail"))
	if code != 1 {
		t.Fatalf("fail gate: exit=%d, ожидалось 1; out:\n%s", code, out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("FAIL-строка не найдена:\n%s", out)
	}
}
