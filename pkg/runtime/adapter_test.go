package runtime

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeAdapterRegistered(t *testing.T) {
	adapter, err := Adapter("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "opencode" {
		t.Errorf("expected opencode, got %s", adapter.Name())
	}
	if got := AdapterNames(); !strings.Contains(strings.Join(got, ","), "opencode") {
		t.Errorf("AdapterNames() = %v, want opencode registered", got)
	}
}

func TestAdapterResolutionByBinaryName(t *testing.T) {
	adapter, err := Adapter(filepath.Base("/usr/local/bin/opencode"))
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "opencode" {
		t.Errorf("expected opencode, got %s", adapter.Name())
	}
}

func TestAdapterUnknownName(t *testing.T) {
	if _, err := Adapter("no-such-runtime"); err == nil {
		t.Fatal("expected error for unknown adapter")
	}
	if AdapterExists("no-such-runtime") {
		t.Fatal("AdapterExists should be false for unknown adapter")
	}
}

func TestOpenCodeAdapterAllowsDeclaredRequest(t *testing.T) {
	adapter, _ := Adapter("opencode")
	launch := Launch{Model: "gpt-5", Effort: "high", Interactive: true, AskQuestions: true, RequireIsolation: true}
	if err := adapter.Validate(launch); err != nil {
		t.Fatalf("opencode must accept its declared capabilities: %v", err)
	}
}

func TestOpenCodeAdapterReadsDefinedCapabilities(t *testing.T) {
	adapter, _ := Adapter("opencode")
	for _, capability := range []Capability{
		CapModelSelection, CapEffortMapping, CapPromptFile, CapSessionIsolation,
	} {
		found := false
		for _, declared := range adapter.Describe().Capabilities {
			if declared == capability {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("opencode should declare capability %q", capability)
		}
	}
}

// restrictedAdapter объявляет только prompt-file/isolation — использование
// model/effort должно блокироваться fail-closed.
type restrictedAdapter struct {
	name         string
	capabilities []Capability
}

func (a restrictedAdapter) Name() string { return a.name }

func (a restrictedAdapter) Describe() Descriptor {
	caps := a.capabilities
	if caps == nil {
		caps = []Capability{CapPromptFile, CapSessionIsolation}
	}
	return Descriptor{
		Name: a.name, Binary: a.name,
		Capabilities: caps,
	}
}

func (a restrictedAdapter) Validate(launch Launch) error { return ValidateLaunch(a, launch) }

func (restrictedAdapter) Command(cli string, launch Launch, promptFile string) ([]string, error) {
	return []string{cli, promptFile}, nil
}

func (restrictedAdapter) Environment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error) {
	return nil, func() {}, nil
}

func TestValidateFailClosedRejectsUndeclaredRequest(t *testing.T) {
	a := restrictedAdapter{name: "restricted-a"}
	for name, launch := range map[string]Launch{
		"model":  {Model: "gpt-5", RequireIsolation: true},
		"effort": {Effort: "high", RequireIsolation: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := a.Validate(launch)
			if err == nil {
				t.Fatalf("undeclared capability must be rejected fail-closed, got nil")
			}
			var missing *MissingCapabilitiesError
			if !errors.As(err, &missing) {
				t.Fatalf("expected *MissingCapabilitiesError, got %T: %v", err, err)
			}
		})
	}
}

func TestValidateSessionIsolationMandatoryOnRequest(t *testing.T) {
	a := restrictedAdapter{name: "restricted-b", capabilities: []Capability{CapPromptFile}}
	if err := a.Validate(Launch{RequireIsolation: true}); err == nil {
		t.Fatal("stage requiring session isolation must fail against an adapter that lacks it")
	}
	if err := a.Validate(Launch{}); err != nil {
		t.Fatalf("empty launch only requires prompt-file (declared): %v", err)
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate adapter registration must panic")
		}
	}()
	RegisterAdapter(&OpenCodeAdapter{})
}

func TestValidateLaunchModelCapability(t *testing.T) {
	a := restrictedAdapter{name: "restricted-c"}
	if err := ValidateLaunchWith(a, Launch{RequireIsolation: true}, []Capability{CapModelSelection}); err == nil {
		t.Fatal("requesting an undeclared capability set must fail")
	}
	if err := ValidateLaunchWith(a, Launch{RequireIsolation: true}, []Capability{CapPromptFile}); err != nil {
		t.Fatalf("declared prompt-file capability must validate: %v", err)
	}
}
