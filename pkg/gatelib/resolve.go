package gatelib

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// StageContext is what a definition's applicability is checked against.
type StageContext struct {
	// Result is the stage result type: md, link or approve.
	Result string
	// LinkKind is pr, build or other for result link.
	LinkKind string
	// Lenient skips the result-type and link-kind applicability checks. It is
	// used for chains translated from schema v5, which never checked whether a
	// rule fits the result type (the rule reports skipped or failed at run
	// time). Parameters are still validated.
	Lenient bool
}

// Resolved is a reference bound to its exact pinned definition.
type Resolved struct {
	Ref        workflow.GateRef
	Definition workflow.GateDefinition
}

// Resolve checks one reference against the library: the exact ID and version
// exist, the definition applies to the stage result, and every parameter is
// declared, correctly typed and within bounds with no required one missing.
// Errors name the parameter and the expected type, never its value.
func (r *Registry) Resolve(ref workflow.GateRef, stage StageContext) (Resolved, error) {
	if err := ref.ValidateShape(); err != nil {
		return Resolved{}, err
	}
	def, err := r.Lookup(ref.Gate, ref.Version)
	if err != nil {
		return Resolved{}, err
	}
	if !stage.Lenient && len(def.ResultTypes) > 0 && !contains(def.ResultTypes, stage.Result) {
		return Resolved{}, fmt.Errorf("гейт %s применим только к result %s, у этапа result %q", def.Ref(), strings.Join(def.ResultTypes, "|"), stage.Result)
	}
	if !stage.Lenient && stage.Result == "link" && len(def.LinkKinds) > 0 && !contains(def.LinkKinds, stage.LinkKind) {
		return Resolved{}, fmt.Errorf("гейт %s применим только к link_kind %s, у этапа link_kind %q", def.Ref(), strings.Join(def.LinkKinds, "|"), stage.LinkKind)
	}
	if err := ValidateParams(def, ref.Params); err != nil {
		return Resolved{}, err
	}
	return Resolved{Ref: ref, Definition: def}, nil
}

// ResolveGroups resolves every reference of a stage's exit chain and returns
// all problems (not just the first), each prefixed with its exit_gate_groups
// path relative to the stage.
func (r *Registry) ResolveGroups(groups workflow.GateGroups, stage StageContext) ([][]Resolved, []error) {
	errs := groups.ValidateShape()
	out := make([][]Resolved, len(groups))
	for i, group := range groups {
		for j, ref := range group {
			if ref.ValidateShape() != nil {
				continue // already reported by ValidateShape
			}
			resolved, err := r.Resolve(ref, stage)
			if err != nil {
				errs = append(errs, fmt.Errorf("exit_gate_groups[%d][%d]: %w", i, j, err))
				continue
			}
			out[i] = append(out[i], resolved)
		}
	}
	return out, errs
}

// ValidateParams validates parameters against the definition's schema.
func ValidateParams(def workflow.GateDefinition, params map[string]any) error {
	declared := make(map[string]workflow.GateParam, len(def.Params))
	for _, param := range def.Params {
		declared[param.Name] = param
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		param, ok := declared[name]
		if !ok {
			accepted := make([]string, 0, len(def.Params))
			for _, p := range def.Params {
				accepted = append(accepted, p.Name)
			}
			if len(accepted) == 0 {
				return fmt.Errorf("гейт %s не принимает параметров, получен %q", def.Ref(), name)
			}
			return fmt.Errorf("гейт %s: неизвестный параметр %q (допустимы: %s)", def.Ref(), name, strings.Join(accepted, ", "))
		}
		if err := checkParam(def, param, params[name]); err != nil {
			return err
		}
	}
	for _, param := range def.Params {
		if _, ok := params[param.Name]; param.Required && !ok {
			return fmt.Errorf("гейт %s: обязательный параметр %q не задан (%s)", def.Ref(), param.Name, param.Type)
		}
	}
	return nil
}

func checkParam(def workflow.GateDefinition, param workflow.GateParam, value any) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("гейт %s: параметр %q %s", def.Ref(), param.Name, fmt.Sprintf(format, args...))
	}
	switch param.Type {
	case workflow.GateParamString:
		text, ok := value.(string)
		if !ok {
			return bad("должен быть строкой")
		}
		if strings.TrimSpace(text) == "" || strings.TrimSpace(text) != text {
			return bad("не должен быть пустым или с пробелами по краям")
		}
	case workflow.GateParamEnum:
		text, ok := value.(string)
		if !ok || !contains(param.Enum, text) {
			return bad("должен быть одним из: %s", strings.Join(param.Enum, ", "))
		}
	case workflow.GateParamBool:
		if _, ok := value.(bool); !ok {
			return bad("должен быть true или false")
		}
	case workflow.GateParamInt:
		number, ok := asInt(value)
		if !ok {
			return bad("должен быть целым числом")
		}
		if param.Min != nil && number < *param.Min {
			return bad("не может быть меньше %d", *param.Min)
		}
	case workflow.GateParamStringList:
		items, ok := asStringList(value)
		if !ok {
			return bad("должен быть списком строк")
		}
		if len(items) == 0 {
			return bad("не может быть пустым списком")
		}
		seen := map[string]bool{}
		for _, item := range items {
			if strings.TrimSpace(item) == "" || strings.TrimSpace(item) != item || seen[item] {
				return bad("содержит пустое, повторяющееся значение или пробелы по краям")
			}
			seen[item] = true
		}
	}
	return nil
}

// StringListParam returns a string_list parameter (nil when unset).
func StringListParam(params map[string]any, name string) []string {
	items, _ := asStringList(params[name])
	return items
}

// StringParam returns a string or enum parameter ("" when unset).
func StringParam(params map[string]any, name string) string {
	text, _ := params[name].(string)
	return text
}

// IntParam returns an int parameter (0 when unset).
func IntParam(params map[string]any, name string) int {
	number, _ := asInt(params[name])
	return number
}

func asInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		if number == math.Trunc(number) && math.Abs(number) < 1<<31 {
			return int(number), true
		}
	}
	return 0, false
}

func asStringList(value any) ([]string, bool) {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...), true
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	}
	return nil, false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
