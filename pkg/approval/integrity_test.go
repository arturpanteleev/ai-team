package approval

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolatedKey уводит ключ контроллера в каталог теста: тест не должен ни
// зависеть от ключа разработчика, ни создавать его.
func isolatedKey(t *testing.T) {
	t.Helper()
	t.Setenv(KeyPathEnv, filepath.Join(t.TempDir(), "approval.key"))
}

func approvalPath(target string, value PendingApproval) string {
	return filepath.Join(target, ".ai-team", "state", "approvals", value.RunID, value.ID+".json")
}

func deliveryApproval(t *testing.T, store *Store) PendingApproval {
	t.Helper()
	value, err := store.Create(PendingApproval{
		RunID: "run-qs01", AttemptID: "attempt-1", FromStage: "delivery", ToStage: "delivery",
		Trigger: "delivery_plan", SubjectHash: testSubject,
		RequiredRoles: []string{"release_manager"}, Quorum: QuorumAny,
		Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "delivery", "reject": "delivery"},
		Payload: json.RawMessage(`{"branch":"feat","commit":{"message":"x"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// TestLoadRejectsDecisionForgedOnDisk — сценарий QS-01: агент дописывает
// решение release_manager прямо в файл approval.
func TestLoadRejectsDecisionForgedOnDisk(t *testing.T) {
	isolatedKey(t)
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value := deliveryApproval(t, store)
	path := approvalPath(target, value)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["status"] = string(StatusResolved)
	record["resolved_action"] = "approve"
	record["resolved_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["decisions"] = []map[string]any{{
		"approval_id": value.ID, "actor_id": "the-llm", "actor_role": "release_manager",
		"action": "approve", "subject_hash": value.SubjectHash,
		"decided_at": time.Now().UTC().Format(time.RFC3339Nano),
	}}
	forged, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, forged, 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(value.RunID, value.ID)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("подделанное решение принято: value=%+v err=%v", loaded, err)
	}
	if !strings.Contains(err.Error(), "изменено на диске") {
		t.Fatalf("сообщение не объясняет причину отказа: %v", err)
	}
	// List/Decide идут через Load и обязаны отказывать так же.
	if _, listErr := store.List(value.RunID); !errors.Is(listErr, ErrIntegrity) {
		t.Fatalf("List принял подделанную запись: %v", listErr)
	}
	if _, decideErr := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "human", ActorRole: "release_manager", Action: "approve", SubjectHash: value.SubjectHash,
	}); !errors.Is(decideErr, ErrIntegrity) {
		t.Fatalf("Decide принял подделанную запись: %v", decideErr)
	}
}

// TestLoadRejectsRecordWrittenWithoutController — запись, созданная с нуля
// мимо контроллера, синтаксически безупречна и обязана быть отвергнута.
func TestLoadRejectsRecordWrittenWithoutController(t *testing.T) {
	isolatedKey(t)
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	forged := PendingApproval{
		SchemaVersion: SchemaVersion, RunID: "run-forged", AttemptID: "attempt-1",
		FromStage: "delivery", ToStage: "delivery", Trigger: "delivery_plan",
		SubjectHash: testSubject, RequiredRoles: []string{"release_manager"}, Quorum: QuorumAny,
		Actions: []string{"approve", "reject"},
		Targets: map[string]string{"approve": "delivery", "reject": "delivery"},
		Status:  StatusResolved, ResolvedAction: "approve", CreatedAt: now, ResolvedAt: now,
		Decisions: []Decision{{
			ApprovalID: NewID("run-forged", "attempt-1", "delivery", "delivery", "delivery_plan", testSubject),
			ActorID:    "the-llm", ActorRole: "release_manager", Action: "approve",
			SubjectHash: testSubject, DecidedAt: now,
		}},
	}
	forged.ID = forged.Decisions[0].ApprovalID
	if validateErr := validate(forged); validateErr != nil {
		t.Fatalf("подделка обязана быть семантически валидной, иначе тест проверяет не то: %v", validateErr)
	}
	data, err := json.MarshalIndent(forged, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(target, ".ai-team", "state", "approvals", forged.RunID)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, forged.ID+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(forged.RunID, forged.ID); !errors.Is(err, ErrIntegrity) ||
		!strings.Contains(err.Error(), "не подписана") {
		t.Fatalf("неподписанная запись принята: %v", err)
	}
}

// TestLoadRejectsRecordSignedByForeignKey — подделка вместе с собственным
// MAC: ключ у контроллера другой, и это обязано быть названо явно.
func TestLoadRejectsRecordSignedByForeignKey(t *testing.T) {
	isolatedKey(t)
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value := deliveryApproval(t, store)
	foreignSecret := make([]byte, keySize)
	for index := range foreignSecret {
		foreignSecret[index] = byte(index + 1)
	}
	foreign := macKey{secret: foreignSecret, id: keyID(foreignSecret), path: "foreign"}
	value.Status = StatusResolved
	value.ResolvedAction = "approve"
	value.ResolvedAt = time.Now().UTC()
	value.Decisions = []Decision{{
		ApprovalID: value.ID, ActorID: "the-llm", ActorRole: "release_manager",
		Action: "approve", SubjectHash: value.SubjectHash, DecidedAt: value.ResolvedAt,
	}}
	signed, err := foreign.sign(value)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath(target, value), data, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(value.RunID, value.ID)
	if !errors.Is(err, ErrIntegrity) || !strings.Contains(err.Error(), "подписана ключом") {
		t.Fatalf("запись, подписанная чужим ключом, принята: %v", err)
	}
}

// TestLegitimateDecisionSurvivesRoundTrip — легитимный путь не меняется:
// решение записывается контроллером, читается обратно и остаётся валидным,
// включая payload, который на диске лежит переиндентированным.
func TestLegitimateDecisionSurvivesRoundTrip(t *testing.T) {
	isolatedKey(t)
	target := t.TempDir()
	store, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	value := deliveryApproval(t, store)
	if _, err := store.Load(value.RunID, value.ID); err != nil {
		t.Fatalf("свежесозданная запись не читается: %v", err)
	}
	resolved, err := store.Decide(value.RunID, value.ID, Decision{
		ActorID: "local-user", ActorRole: "release_manager", Action: "approve",
		SubjectHash: value.SubjectHash,
	})
	if err != nil || resolved.Status != StatusResolved {
		t.Fatalf("легитимное решение отклонено: %+v err=%v", resolved, err)
	}
	loaded, err := store.Load(value.RunID, value.ID)
	if err != nil {
		t.Fatalf("решение контроллера не читается обратно: %v", err)
	}
	if loaded.ResolvedAction != "approve" || loaded.Integrity == nil || loaded.Integrity.Alg != MACAlgorithm {
		t.Fatalf("неожиданная запись после round-trip: %+v", loaded)
	}
	// Другой процесс с тем же ключом (CLI decision, web) читает запись так же.
	second, err := NewStore(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Load(value.RunID, value.ID); err != nil {
		t.Fatalf("второй store с тем же ключом не принял запись: %v", err)
	}
	list, err := second.List(value.RunID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %+v err=%v", list, err)
	}
}

func TestNewStoreRejectsKeyInsideTarget(t *testing.T) {
	target := t.TempDir()
	t.Setenv(KeyPathEnv, filepath.Join(target, ".ai-team", "approval.key"))
	if _, err := NewStore(target); err == nil || !strings.Contains(err.Error(), "внутри target") {
		t.Fatalf("ключ внутри target принят: %v", err)
	}
}

func TestNewStoreRejectsWorldReadableKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-режим файла неприменим")
	}
	keyPath := filepath.Join(t.TempDir(), "approval.key")
	secret := strings.Repeat("ab", keySize)
	if err := os.WriteFile(keyPath, []byte(secret+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyPathEnv, keyPath)
	if _, err := NewStore(t.TempDir()); err == nil || !strings.Contains(err.Error(), "доступен группе") {
		t.Fatalf("ключ со слабыми правами принят: %v", err)
	}
}

func TestKeyIsCreatedOnceAndReused(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "nested", "approval.key")
	t.Setenv(KeyPathEnv, keyPath)
	first, err := loadOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != keySize {
		t.Fatalf("формат ключа: %d байт err=%v", len(decoded), err)
	}
	second, err := loadOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.id != second.id {
		t.Fatalf("существующий ключ перезаписан: %s != %s", first.id, second.id)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("ключ создан с правами %04o", info.Mode().Perm())
	}
}
