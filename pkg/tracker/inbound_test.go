package tracker

import "testing"

func TestResolveInbound(t *testing.T) {
	rules := []InboundRule{
		{Status: "In Progress", Action: ActionAdvance},
		{Status: "Done", Action: ActionAdvance},
		{Status: "done", Action: ActionReturn, Stage: "code_review", ToStage: "implementation"},
		{Status: "Rework", Action: ActionReturn, Stage: "code_review", ToStage: "implementation"},
		{Status: "Rework", Action: ActionReturn, Stage: "qa", ToStage: "implementation"},
		{Status: "Same", Action: ActionAdvance},
		{Status: "same", Action: ActionAdvance, Stage: "qa"},
	}
	cases := []struct {
		name, status, stage string
		want                Resolution
		action              string
	}{
		{"case-insensitive match", "in progress", "qa", ResolutionMatch, ActionAdvance},
		{"unmapped", "Blocked", "qa", ResolutionUnmapped, ""},
		{"wildcard only at another stage", "Done", "qa", ResolutionMatch, ActionAdvance},
		{"wildcard overlaps scoped rule", " DONE ", "code_review", ResolutionAmbiguous, ""},
		{"stage-scoped rule", "Rework", "code_review", ResolutionMatch, ActionReturn},
		{"scoped rule at another stage", "Rework", "deploy", ResolutionUnmapped, ""},
		{"agreeing rules are not ambiguous", "Same", "qa", ResolutionMatch, ActionAdvance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, got, _ := ResolveInbound(rules, tc.status, tc.stage)
			if got != tc.want || (got == ResolutionMatch && rule.Action != tc.action) {
				t.Fatalf("got %v %+v, want %v %s", got, rule, tc.want, tc.action)
			}
		})
	}
}

func TestIsOwnEcho(t *testing.T) {
	if !IsOwnEcho(Change{Origin: OriginAITeam}) || IsOwnEcho(Change{Origin: OriginExternal}) || IsOwnEcho(Change{}) {
		t.Fatal("only ai-team origin is an echo; an unknown origin is treated as external")
	}
}
