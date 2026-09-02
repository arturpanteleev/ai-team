package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const junitPassReportingXML = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="demo" tests="2" failures="0">
  <testsuite name="demo.test" tests="2" failures="0">
    <testcase classname="demo" name="test_add" time="0.001"/>
    <testcase classname="demo" name="test_sub" time="0.001"/>
  </testsuite>
</testsuites>
`

func writeJunitReport(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "reports"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reports", "pass.xml"), []byte(junitPassReportingXML), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestJUnitAdapterPassingReport(t *testing.T) {
	dir := writeJunitReport(t)
	command := []string{"cp", filepath.Join(dir, "reports", "pass.xml"), "report.xml"}
	result, err := (Runner{TargetDir: dir}).Run(context.Background(), Definition{
		Name: "junit", Class: "unit", Adapter: AdapterJUnit,
		Command: command, Policy: PolicyRequired, ReportFile: "report.xml",
	})
	if err != nil {
		t.Fatalf("passing junit: %v", err)
	}
	if result.Status != StatusPassed || result.DiscoveredTests != 2 || result.PassedTests != 2 {
		t.Fatalf("passing result = %+v", result)
	}
	if result.StructuredOutputSHA256 == "" || result.StructuredOutputBytes == 0 {
		t.Fatalf("digest/bytes не заполнены: %+v", result)
	}
	if !IsTestEvidence(result) {
		t.Fatalf("прошедший junit required unit check должен быть test evidence")
	}
	// V0-6 should-fix: в записи обязан быть полный WorkspaceDigest (равный
	// полному fingerprint текущего workspace с report на диске), а не
	// excluding-дайджест. Верификаторы пересчитывают полный digest и требуют
	// равенство по обоим полям.
	full, err := WorkspaceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceDigestBefore != full || result.WorkspaceDigestAfter != full {
		t.Fatalf("digest в записи = before %q after %q, ожидался полный %q",
			result.WorkspaceDigestBefore, result.WorkspaceDigestAfter, full)
	}
	if !VerifyResultDigest(result) {
		t.Fatalf("evidence digest не самосогласован: %+v", result)
	}
}

func TestJUnitAdapterFailingReportFailsOnExitZero(t *testing.T) {
	dir := writeJunitReport(t)
	if err := os.WriteFile(filepath.Join(dir, "failing.xml"), []byte(pytestFixture()), 0644); err != nil {
		t.Fatal(err)
	}
	// cp завершается 0 даже при падении тестов в отчёте — adapter обязан
	// провалить check на основе XML (maven surefire semantics).
	result, err := (Runner{TargetDir: dir}).Run(context.Background(), Definition{
		Name: "junit", Class: "unit", Adapter: AdapterJUnit,
		Command: []string{"cp", filepath.Join(dir, "failing.xml"), "report.xml"}, Policy: PolicyRequired,
		ReportFile: "report.xml",
	})
	if err == nil {
		t.Fatalf("отчёт с failure обязан фейлить required check; result=%+v", result)
	}
	if result.Status != StatusFailed || result.FailedTests != 1 {
		t.Fatalf("result = %+v", result)
	}
	if IsTestEvidence(result) {
		t.Fatalf("failed junit не должен быть test evidence")
	}
}

func TestJUnitAdapterZeroTestsRejected(t *testing.T) {
	dir := writeJunitReport(t)
	if err := os.WriteFile(filepath.Join(dir, "zero.xml"), []byte(zeroFixture()), 0644); err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		Name: "junit", Class: "unit", Adapter: AdapterJUnit,
		Command: []string{"cp", filepath.Join(dir, "zero.xml"), "report.xml"}, Policy: PolicyRequired,
		ReportFile: "report.xml",
	}
	result, err := (Runner{TargetDir: dir}).Run(context.Background(), definition)
	if err == nil {
		t.Fatalf("zero-test отчёт обязан фейлить; result=%+v", result)
	}
	if result.Status != StatusFailed || !strings.Contains(result.Reason, "не содержит") {
		t.Fatalf("result = %+v reason=%q", result, result.Reason)
	}
}

func TestJUnitAdapterMissingReportFailed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nowhere.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Команда не создаёт report.xml.
	definition := Definition{
		Name: "junit", Class: "unit", Adapter: AdapterJUnit,
		Command: []string{"cp", filepath.Join(dir, "nowhere.txt"), "other.txt"}, Policy: PolicyRequired,
		ReportFile: "report.xml",
	}
	result, err := (Runner{TargetDir: dir}).Run(context.Background(), definition)
	if err == nil {
		t.Fatalf("отсутствующий report обязан фейлить; result=%+v", result)
	}
	if result.Status != StatusFailed {
		t.Fatalf("result = %+v", result)
	}
}

func TestJUnitDefinitionValidation(t *testing.T) {
	base := Definition{Name: "j", Class: "unit", Adapter: AdapterJUnit,
		Command: []string{"pytest"}, Policy: PolicyRequired, ReportFile: "report.xml"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid junit def: %v", err)
	}

	noReport := base
	noReport.ReportFile = ""
	if err := noReport.Validate(); err == nil {
		t.Fatal("junit без report_file должен отклоняться")
	}

	escaping := base
	escaping.ReportFile = "../report.xml"
	if err := escaping.Validate(); err == nil {
		t.Fatal("report_file вне target должен отклоняться")
	}

	absReport := base
	absReport.ReportFile = "/tmp/report.xml"
	if err := absReport.Validate(); err == nil {
		t.Fatal("absolute report_file должен отклоняться")
	}

	badClass := base
	badClass.Class = "lint"
	if err := badClass.Validate(); err == nil {
		t.Fatal("junit только для test class")
	}

	commandAdapter := Definition{Name: "c", Class: "unit", Adapter: AdapterCommand,
		Command: []string{"echo", "hi"}, Policy: PolicyRequired, ReportFile: "report.xml"}
	if err := commandAdapter.Validate(); err == nil {
		t.Fatal("report_file запрещён для command adapter")
	}
}

func pytestFixture() string {
	return `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" tests="1" failures="1" errors="0" skipped="0">
    <testcase classname="test_calc" name="test_div" time="0.002">
      <failure message="AssertionError: division by zero" type="AssertionError"/>
    </testcase>
  </testsuite>
</testsuites>
`
}

func zeroFixture() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="no tests">
  <testsuite name="empty" tests="0" failures="0" errors="0" skipped="0">
    <system-out/>
  </testsuite>
</testsuites>
`
}

func TestJUnitAdapterOptionalFailureIsExplicit(t *testing.T) {
	dir := writeJunitReport(t)
	if err := os.WriteFile(filepath.Join(dir, "failing.xml"), []byte(pytestFixture()), 0644); err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		Name: "junit", Class: "e2e", Adapter: AdapterJUnit,
		Command: []string{"cp", filepath.Join(dir, "failing.xml"), "report.xml"}, Policy: PolicyOptional,
		ReportFile: "report.xml",
	}
	result, err := (Runner{TargetDir: dir}).Run(context.Background(), definition)
	if err != nil {
		t.Fatalf("optional: %v", err)
	}
	if result.Status != StatusFailed {
		t.Fatalf("optional failed result = %+v", result)
	}
}
