package evidence

import (
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

func TestValidAttemptUsageTariffInputs(t *testing.T) {
	for name, test := range map[string]struct {
		usage workflow.AttemptUsage
		valid bool
	}{
		"legacy usage":           {workflow.AttemptUsage{Attested: true, TokensInput: 5}, true},
		"model and cache":        {workflow.AttemptUsage{Attested: true, TokensInput: 5, Model: "model-a", CacheReadTokens: 3, CacheWriteTokens: 2}, true},
		"included cache fits":    {workflow.AttemptUsage{Attested: true, TokensInput: 5, CacheReadTokens: 5, CacheReadIncludedInInput: true}, true},
		"included cache exceeds": {workflow.AttemptUsage{Attested: true, TokensInput: 5, CacheReadTokens: 6, CacheReadIncludedInInput: true}, false},
		"negative cache":         {workflow.AttemptUsage{Attested: true, CacheWriteTokens: -1}, false},
		"model without attest":   {workflow.AttemptUsage{Model: "model-a"}, false},
		"control char in model":  {workflow.AttemptUsage{Attested: true, Model: "model\n"}, false},
	} {
		if got := validAttemptUsageTariffInputs(test.usage); got != test.valid {
			t.Errorf("%s: valid=%v; want %v", name, got, test.valid)
		}
	}
}
