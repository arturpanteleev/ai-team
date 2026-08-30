package junit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Golden-fixture матрица: pytest, Jest, Gradle, Maven. Counts выводятся из
// реальных <testcase> элементов, а не из атрибутов-агрегатов — их заполняют
// инструменты по-разному, поэтому каждый ключевой индикатор поджат вручную.
func TestGoldenFixtures(t *testing.T) {
	cases := []struct {
		name             string
		file             string
		suites, tests    int
		failures, errors int
		skipped, passed  int
	}{
		{"pytest", "pytest.xml", 1, 4, 1, 0, 1, 2},
		{"jest", "jest.xml", 2, 5, 1, 0, 0, 4},
		{"gradle", "gradle.xml", 1, 3, 0, 1, 0, 2},
		{"maven", "maven.xml", 1, 5, 1, 1, 1, 2},
		{"zero", "zero.xml", 1, 0, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Parse(loadFixture(t, tc.file))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if report.Suites != tc.suites || report.Tests != tc.tests ||
				report.Failures != tc.failures || report.Errors != tc.errors ||
				report.Skipped != tc.skipped || report.Passed() != tc.passed {
				t.Fatalf("counts = %+v (passed=%d), ожидалось suites=%d tests=%d failures=%d errors=%d skipped=%d passed=%d",
					report, report.Passed(), tc.suites, tc.tests, tc.failures, tc.errors, tc.skipped, tc.passed)
			}
		})
	}
}

func TestParseRejectsDoctype(t *testing.T) {
	if _, err := Parse(loadFixture(t, "doctype.xml")); err == nil {
		t.Fatal("DOCTYPE должен отклоняться")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	if _, err := Parse([]byte("<testsuites><testsuite><testcase")); err == nil {
		t.Fatal("malformed XML должен отклоняться")
	}
	if _, err := Parse([]byte("not-xml-at-all")); err == nil {
		t.Fatal("не-XML должен отклоняться")
	}
	if _, err := Parse([]byte("<testsuites></testcase></testsuites>")); err == nil {
		t.Fatal("mismatched закрытие должен отклоняться")
	}
}

func TestParseRequiresSuiteRoot(t *testing.T) {
	if _, err := Parse([]byte("<foo><bar/></foo>")); err == nil {
		t.Fatal("корень без testsuite/testsuites должен отклоняться")
	}
}

func TestParseRejectsDeepNesting(t *testing.T) {
	deep := "<testsuites>" + strings.Repeat("<a>", 64) + "<testcase/>" + strings.Repeat("</a>", 64) + "</testsuites>"
	if _, err := Parse([]byte(deep)); err == nil {
		t.Fatal("глубокая вложенность должна отклоняться")
	}
}

func TestParseRejectsEntityInsideContent(t *testing.T) {
	data := []byte("<testsuites><testsuite name=\"entity\"><testcase name=\"up\" classname=\"x\">&xxe;</testcase></testsuite></testsuites>")
	if _, err := Parse(data); err == nil {
		t.Fatal("неразрешённая entity должна отклоняться xml.Decoder")
	}
}

func TestParseEmptyReport(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("пустой отчёт должен отклоняться")
	}
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("пустой отчёт должен отклоняться")
	}
}

func TestParseProcessingInstructionRejected(t *testing.T) {
	data := []byte("<?xml-stylesheet href=\"x\"?><testsuites><testsuite name=\"s\"><testcase name=\"t\"/></testsuite></testsuites>")
	if _, err := Parse(data); err == nil {
		t.Fatal("processing instruction должна отклоняться")
	}
}
