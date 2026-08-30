// Package junit реализует bounded strict parser JUnit XML отчётов
// (V0-6): counts suites/testcases/failures/errors/skips выводятся из реальных
// <testcase> элементов, а не из атрибутов-агрегатов (jest/maven/gradle
// заполняют их непоследовательно). DOCTYPE и entity-конструкции запрещены
// (не-разрешаемая внешняя подмена), глубина и размер ограничены.
package junit

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxReportSize — предел сырого JUnit XML (зеркалит maxStructuredOutput).
const MaxReportSize = 64 << 20

const maxDepth = 32

// Report — детерминированный счётный вердикт JUnit отчёта.
type Report struct {
	Suites   int `json:"suites"`
	Tests    int `json:"tests"`
	Failures int `json:"failures"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
}

// Passed — число успешно выполненных test cases.
func (r *Report) Passed() int {
	return r.Tests - r.Failures - r.Errors - r.Skipped
}

// Parse строго разбирает JUnit XML. Поддерживаются корни <testsuites> и
// <testsuite> (в т.ч. вложенные testsuite внутри testsuites).
func Parse(data []byte) (*Report, error) {
	if len(data) == 0 {
		return nil, errors.New("junit: пустой отчёт")
	}
	if len(data) > MaxReportSize {
		return nil, fmt.Errorf("junit: отчёт превышает %d байт", MaxReportSize)
	}
	lower := bytes.ToLower(data)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return nil, errors.New("junit: DOCTYPE/entity недопустимы")
	}

	report := &Report{}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true

	// Стек открытых testcase для точного приписывания failure/error/skipped.
	type openCase struct{ failure, err, skipped bool }
	cases := make([]*openCase, 0, 8)

	depth := 0
	sawSuite := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("junit: %w", err)
		}
		switch current := token.(type) {
		case xml.StartElement:
			depth++
			if depth > maxDepth {
				return nil, fmt.Errorf("junit: вложенность превышает %d", maxDepth)
			}
			switch current.Name.Local {
			case "testsuites", "testsuite":
				sawSuite = true
			case "testcase":
				cases = append(cases, &openCase{})
			case "failure":
				if len(cases) > 0 {
					cases[len(cases)-1].failure = true
				}
			case "error":
				if len(cases) > 0 {
					cases[len(cases)-1].err = true
				}
			case "skipped":
				if len(cases) > 0 {
					cases[len(cases)-1].skipped = true
				}
			}
		case xml.EndElement:
			switch current.Name.Local {
			case "testcase":
				if len(cases) > 0 {
					open := cases[len(cases)-1]
					cases = cases[:len(cases)-1]
					report.Tests++
					switch {
					case open.failure:
						report.Failures++
					case open.err:
						report.Errors++
					case open.skipped:
						report.Skipped++
					}
				}
			case "testsuite":
				report.Suites++
			}
			depth--
		case xml.CharData:
		case xml.Comment:
		case xml.Directive:
			return nil, errors.New("junit: xml directive недопустима")
		case xml.ProcInst:
			if strings.EqualFold(current.Target, "xml") {
				continue
			}
			return nil, errors.New("junit: processing instruction недопустима")
		}
	}
	if !sawSuite {
		return nil, errors.New("junit: нет корневого testsuite/testsuites")
	}
	return report, nil
}
