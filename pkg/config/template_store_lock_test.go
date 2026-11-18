package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTemplateStorePublishSerializesAcrossStores models two processes (the web
// control plane and a CLI/worker) publishing against the same project through
// separate TemplateStore instances. Only one may win with a shared
// expectedVersion; the other must observe the advanced version and conflict.
func TestTemplateStorePublishSerializesAcrossStores(t *testing.T) {
	if !publishLockSupported {
		t.Skip("inter-process publish lock is a no-op on this platform")
	}

	target := t.TempDir()
	controlDir := filepath.Join(target, ".ai-team")
	if err := os.Mkdir(controlDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	firstYAML, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "config.yaml"), firstYAML, 0644); err != nil {
		t.Fatal(err)
	}

	// Two stores: no in-process mutex is shared between them, so only the file
	// lock can keep their read-modify-write cycles from overlapping.
	storeA, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, expectedVersion, err := storeA.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}

	withTitle := func(title string) []byte {
		profile := *initial
		profile.Title = title
		data, marshalErr := profile.Marshal()
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return data
	}
	payloadA := withTitle("及时进行发布 A")
	payloadB := withTitle("及时进行发布 B")
	if TemplateVersionID(payloadA) == TemplateVersionID(payloadB) {
		t.Fatal("test payloads must have distinct versions")
	}

	type outcome struct {
		version string
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	publish := func(store *TemplateStore, data []byte) {
		<-start
		version, publishErr := store.Publish(data, expectedVersion)
		results <- outcome{version: version, err: publishErr}
	}
	go publish(storeA, payloadA)
	go publish(storeB, payloadB)
	close(start)
	first := <-results
	second := <-results

	var winner outcome
	var conflict error
	succeeded := 0
	for _, result := range []outcome{first, second} {
		if result.err == nil {
			succeeded++
			winner = result
		} else {
			conflict = result.err
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly one successful publish, got %d: first=%+v second=%+v", succeeded, first, second)
	}
	if conflict == nil || !strings.Contains(conflict.Error(), "активный шаблон изменился") {
		t.Fatalf("competing publish must report a version conflict, got %v", conflict)
	}

	winnerData := payloadA
	if winner.version == TemplateVersionID(payloadB) {
		winnerData = payloadB
	}
	if winner.version != TemplateVersionID(winnerData) {
		t.Fatalf("winner returned version %q not matching either payload", winner.version)
	}

	active, activeVersion, err := storeA.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if activeVersion != winner.version || !bytes.Equal(active, winnerData) {
		t.Fatalf("active template does not match the winning publish: version=%q want=%q", activeVersion, winner.version)
	}

	versions, err := storeA.ListVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].ID != winner.version {
		t.Fatalf("version history damaged: %+v", versions)
	}

	// The provenance audit written by Publish must describe the winning bytes
	// and keep the original hand-written file as the trusted baseline.
	webWritten, err := storeA.isWebWritten(active)
	if err != nil || !webWritten {
		t.Fatalf("winning bytes not recorded as web-written: webWritten=%t err=%v", webWritten, err)
	}
	baseline, err := os.ReadFile(storeA.baselinePath())
	if err != nil || !bytes.Equal(baseline, firstYAML) {
		t.Fatalf("trusted baseline changed: baseline=%q err=%v", baseline, err)
	}
}
