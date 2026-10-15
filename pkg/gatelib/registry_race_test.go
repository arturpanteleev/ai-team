package gatelib

import (
	"sync"
	"testing"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// The registry is read by every task of a worker process while a new version
// may be published: concurrent Register/Lookup/Catalog/Clone must be safe
// (run under -race), each ID+version registered exactly once, and a reader
// must never see a half-published definition.
func TestRegistryIsSafeUnderConcurrentUse(t *testing.T) {
	lib := Default().Clone()
	const writers, readers, versions = 8, 8, 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	registered := map[string]int{}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := 2; v < 2+versions; v++ {
				def := workflow.GateDefinition{ID: "race_gate", Version: v, Kind: workflow.GateKindBuiltin, Title: "race gate"}
				if err := lib.Register(def); err == nil {
					mu.Lock()
					registered[def.Ref()]++
					mu.Unlock()
				}
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if def, err := lib.Lookup(GateRequiredSections, 1); err != nil || def.Title == "" {
					t.Errorf("builtin lookup: %v", err)
					return
				}
				for _, def := range lib.Catalog() {
					if def.ID == "" || def.Version <= 0 || def.Title == "" {
						t.Errorf("half-published definition in catalog: %+v", def)
						return
					}
				}
				if latest, ok := lib.Latest("race_gate"); ok && latest.Version < 2 {
					t.Errorf("latest version %d", latest.Version)
					return
				}
				_ = lib.Clone()
			}
		}()
	}
	wg.Wait()
	if len(registered) != versions {
		t.Fatalf("registered %d distinct versions, want %d", len(registered), versions)
	}
	for ref, count := range registered {
		if count != 1 {
			t.Fatalf("%s registered %d times; published definitions are immutable", ref, count)
		}
	}
	if latest, ok := lib.Latest("race_gate"); !ok || latest.Version != 1+versions {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestDefaultRegistryIsSafeForConcurrentFirstUse(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Default().Lookup(GateVerdictMarker, 1); err != nil {
				t.Errorf("lookup: %v", err)
			}
		}()
	}
	wg.Wait()
}
