package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateStorePublishesSingleActiveTemplateAndPinsTasks(t *testing.T) {
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
	configPath := filepath.Join(controlDir, "config.yaml")
	if err := os.WriteFile(configPath, firstYAML, 0644); err != nil {
		t.Fatal(err)
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	_, firstVersion, err := store.ReadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if version, err := store.PinCurrentForRun("run-original", "task-original"); err != nil || version != firstVersion {
		t.Fatalf("pin initial task: version=%q err=%v", version, err)
	}

	updated := *initial
	updated.Title = "Опубликованный новый поток"
	secondYAML, err := updated.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	secondVersion, err := store.Publish(secondYAML, firstVersion)
	if err != nil {
		t.Fatal(err)
	}
	if secondVersion == firstVersion {
		t.Fatal("changed template must receive a new immutable version")
	}
	if _, err := store.Publish(firstYAML, firstVersion); err == nil {
		t.Fatal("publish must reject a stale expected version")
	}

	// A later recovery/continuation run for the same task reuses its task pin,
	// even though the project's one active template has advanced.
	if version, err := store.PinCurrentForRun("run-recovery", "task-original"); err != nil || version != firstVersion {
		t.Fatalf("pin continuation: version=%q err=%v", version, err)
	}
	if version, err := store.PinDataForRun("run-retry", "task-original", secondYAML); err != nil || version != firstVersion {
		t.Fatalf("pin retry with a newer resolved config: version=%q err=%v", version, err)
	}
	pinned, pinnedVersion, found, err := store.ReadPinnedRun("run-recovery")
	if err != nil || !found || pinnedVersion != firstVersion || string(pinned) != string(firstYAML) {
		t.Fatalf("continuation lost original template pin: found=%t version=%q err=%v", found, pinnedVersion, err)
	}
	retryPinned, retryVersion, found, err := store.ReadPinnedRun("run-retry")
	if err != nil || !found || retryVersion != firstVersion || string(retryPinned) != string(firstYAML) {
		t.Fatalf("retry lost original template pin: found=%t version=%q err=%v", found, retryVersion, err)
	}

	// A new task captures the newly published project template.
	if version, err := store.PinCurrentForRun("run-new", "task-new"); err != nil || version != secondVersion {
		t.Fatalf("pin new task: version=%q err=%v", version, err)
	}
	newPinned, newVersion, found, err := store.ReadPinnedRun("run-new")
	if err != nil || !found || newVersion != secondVersion || string(newPinned) != string(secondYAML) {
		t.Fatalf("new task did not use active template: found=%t version=%q err=%v", found, newVersion, err)
	}
	versions, err := store.ListVersions()
	if err != nil || len(versions) != 2 {
		t.Fatalf("published versions=%+v err=%v", versions, err)
	}
}

func TestTemplateStoreDetectsPinTamperingAndRejectsUnsafeIDs(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".ai-team"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".ai-team", "config.yaml"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	store, err := NewTemplateStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinCurrentForRun("../escape", "task"); err == nil {
		t.Fatal("unsafe run id was accepted")
	}
	if _, err := store.PinCurrentForRun("run-one", "task-one"); err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(target, ".ai-team", "task-template-pins", "task-one.yaml")
	if err := os.Chmod(pinPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinPath, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := store.ReadPinnedRun("run-one"); err == nil || found {
		t.Fatalf("tampered task pin accepted: found=%t err=%v", found, err)
	}
}
