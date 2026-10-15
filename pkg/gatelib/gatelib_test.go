package gatelib

import (
	"strings"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestBuiltinLibraryCoversLegacyBehavior(t *testing.T) {
	lib := Default()
	for _, id := range []string{GateRequiredSections, GateVerdictMarker, GatePRExists, GatePROpen, GatePRBaseBranch, GateProjectChecks, GatePRChecksGreen, GateAgentReview, GateHumanApproval} {
		def, err := lib.Lookup(id, 1)
		if err != nil {
			t.Fatalf("%s@1 missing: %v", id, err)
		}
		if def.Title == "" {
			t.Fatalf("%s has no title", id)
		}
	}
	kinds := map[string]workflow.GateKind{GateAgentReview: workflow.GateKindLLM, GateHumanApproval: workflow.GateKindHuman, GateVerdictMarker: workflow.GateKindBuiltin}
	for id, kind := range kinds {
		if def, _ := lib.Lookup(id, 1); def.Kind != kind {
			t.Fatalf("%s kind = %s, want %s", id, def.Kind, kind)
		}
	}
}

func TestPublishedDefinitionsAreImmutable(t *testing.T) {
	lib := Default().Clone()
	def, _ := lib.Lookup(GateVerdictMarker, 1)
	if err := lib.Register(def); err == nil || !strings.Contains(err.Error(), "неизменяемы") {
		t.Fatalf("re-registering must fail: %v", err)
	}
	// Returned definitions are copies: mutating one cannot alter the library.
	def, _ = lib.Lookup(GateRequiredSections, 1)
	def.Params[0].Name = "hacked"
	again, _ := lib.Lookup(GateRequiredSections, 1)
	if again.Params[0].Name != "sections" {
		t.Fatal("Lookup leaked internal state")
	}
	// A clone is independent of the original.
	lib.MustRegister(workflow.GateDefinition{ID: "extra", Version: 1, Kind: workflow.GateKindBuiltin, Title: "Extra"})
	if _, err := Default().Lookup("extra", 1); err == nil {
		t.Fatal("clone registration leaked into the default library")
	}
}

func TestDigestIsStableAndVersionSensitive(t *testing.T) {
	a, _ := Default().Lookup(GateRequiredSections, 1)
	b, _ := Default().Clone().Lookup(GateRequiredSections, 1)
	if Digest(a) != Digest(b) {
		t.Fatal("digest must be deterministic")
	}
	c := a
	c.Version = 2
	if Digest(a) == Digest(c) {
		t.Fatal("digest must depend on the definition body")
	}
}

func TestRegisterRejectsMalformedDefinitions(t *testing.T) {
	cases := map[string]workflow.GateDefinition{
		"bad id":        {ID: "Bad", Version: 1, Kind: workflow.GateKindBuiltin, Title: "x"},
		"zero version":  {ID: "ok", Version: 0, Kind: workflow.GateKindBuiltin, Title: "x"},
		"unknown kind":  {ID: "ok", Version: 1, Kind: "shell", Title: "x"},
		"no title":      {ID: "ok", Version: 1, Kind: workflow.GateKindBuiltin},
		"dup param":     {ID: "ok", Version: 1, Kind: workflow.GateKindBuiltin, Title: "x", Params: []workflow.GateParam{{Name: "a", Type: workflow.GateParamBool}, {Name: "a", Type: workflow.GateParamBool}}},
		"enum no vals":  {ID: "ok", Version: 1, Kind: workflow.GateKindBuiltin, Title: "x", Params: []workflow.GateParam{{Name: "a", Type: workflow.GateParamEnum}}},
		"unknown ptype": {ID: "ok", Version: 1, Kind: workflow.GateKindBuiltin, Title: "x", Params: []workflow.GateParam{{Name: "a", Type: "float"}}},
	}
	for name, def := range cases {
		if err := NewRegistry().Register(def); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLookupErrorsListWhatIsAvailable(t *testing.T) {
	_, err := Default().Lookup("nope", 1)
	if err == nil || !strings.Contains(err.Error(), "required_sections") {
		t.Fatalf("unknown id should list available IDs: %v", err)
	}
	_, err = Default().Lookup(GateVerdictMarker, 5)
	if err == nil || !strings.Contains(err.Error(), "доступны: 1") {
		t.Fatalf("unknown version should list versions: %v", err)
	}
}

func TestResolveChecksParamsAndApplicability(t *testing.T) {
	lib := Default()
	md := StageContext{Result: "md"}
	pr := StageContext{Result: "link", LinkKind: "pr"}
	build := StageContext{Result: "link", LinkKind: "build"}
	ok := func(ref workflow.GateRef, ctx StageContext) {
		t.Helper()
		if _, err := lib.Resolve(ref, ctx); err != nil {
			t.Fatalf("%+v: %v", ref, err)
		}
	}
	bad := func(ref workflow.GateRef, ctx StageContext, want string) {
		t.Helper()
		if _, err := lib.Resolve(ref, ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%+v: want %q, got %v", ref, want, err)
		}
	}
	ok(workflow.GateRef{Gate: GateRequiredSections, Version: 1, Params: map[string]any{"sections": []any{"A", "B"}}}, md)
	ok(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": "x", "mode": "grill", "max_rounds": 3}}, md)
	ok(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": "x", "max_rounds": float64(2)}}, pr)
	ok(workflow.GateRef{Gate: GateProjectChecks, Version: 1}, pr)
	ok(workflow.GateRef{Gate: GateHumanApproval, Version: 1}, build)

	bad(workflow.GateRef{Gate: GateProjectChecks, Version: 1}, build, "link_kind pr")
	bad(workflow.GateRef{Gate: GateVerdictMarker, Version: 1}, pr, "только к result md")
	bad(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": ""}}, md, `"agent"`)
	bad(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": "x", "max_rounds": -1}}, md, "меньше 0")
	bad(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": "x", "max_rounds": "3"}}, md, "целым числом")
	bad(workflow.GateRef{Gate: GateAgentReview, Version: 1, Params: map[string]any{"agent": "x", "temperature": 1}}, md, "допустимы: agent, mode, max_rounds")
	bad(workflow.GateRef{Gate: GateRequiredSections, Version: 1, Params: map[string]any{"sections": []any{}}}, md, "пустым списком")
	bad(workflow.GateRef{Gate: GateRequiredSections, Version: 1, Params: map[string]any{"sections": []any{1}}}, md, "списком строк")
	bad(workflow.GateRef{Gate: GateRequiredSections, Version: 1}, md, "обязательный параметр")
	bad(workflow.GateRef{Gate: GateRequiredSections, Version: 0, Params: map[string]any{"sections": []any{"A"}}}, md, "версия обязательна")
}

func TestResolveGroupsReportsEveryProblemWithPaths(t *testing.T) {
	groups := workflow.GateGroups{
		{{Gate: GateVerdictMarker, Version: 1}},
		{{Gate: "ghost", Version: 1}, {Gate: GateHumanApproval, Version: 7}},
	}
	resolved, errs := Default().ResolveGroups(groups, StageContext{Result: "md"})
	if len(errs) != 2 || len(resolved[0]) != 1 || len(resolved[1]) != 0 {
		t.Fatalf("errs=%v resolved=%v", errs, resolved)
	}
	if !strings.HasPrefix(errs[0].Error(), "exit_gate_groups[1][0]:") || !strings.HasPrefix(errs[1].Error(), "exit_gate_groups[1][1]:") {
		t.Fatalf("paths: %v", errs)
	}
}

func TestPinCarriesExactVersionAndDigest(t *testing.T) {
	def, _ := Default().Lookup(GateAgentReview, 1)
	pin := Pin(def)
	if pin.Gate != GateAgentReview || pin.Version != 1 || pin.Kind != workflow.GateKindLLM || len(pin.Digest) != 64 {
		t.Fatalf("pin = %+v", pin)
	}
}
