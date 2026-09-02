// Package provenance содержит authority-bearing identity manifest (V0-2).
// В отличие от snapshots (config.json/workflow.json, покрывающих всё целиком),
// manifest явно раскрывает digest каждого источника истины: runtime-бинарник,
// resolved config surface (provider/model/effort), definition агента, его
// prompt, check suite, base/candidate identity. Значение, которое невозможно
// получить deterministically, помечается "unknown" — никаких догадок.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion 1 связывает manifest с согласованным набором kinds и
// правилами drift-детекции (V0-2).
const SchemaVersion = 1

// Kind — authority-bearing источник истины внутри provenance manifest.
const (
	// KindRuntime — digest исполняемого бинарника контроллера и его runtime
	// identity (go version/goos/goarch, module version, VCS).
	KindRuntime = "runtime"
	// KindConfig — resolved config surface (cli/model/effort по этапам).
	KindConfig = "config"
	// KindAgent — canonical digest definition агента (без prompt).
	KindAgent = "agent_definition"
	// KindPrompt — digest содержимого prompt агента.
	KindPrompt = "prompt"
	// KindCheckSuite — canonical digest списка checks агента.
	KindCheckSuite = "check_suite"
	// KindProviderModel — какая конкретно provider/model/effort исполняет агента.
	KindProviderModel = "provider_model"
	// KindBase — base identity (base commit/tree) до начала run.
	KindBase = "base"
	// KindCandidate — candidate workspace identity (worktree/workspace hash).
	KindCandidate = "candidate"
)

// UnknownValue — значение digest, когда источник не разрешается
// deterministic-способом. Unknown никогда не считаеtтся drift'ом.
const UnknownValue = "unknown"

// Digest — canonical identity значения. Type фиксирует алгоритм (sha256).
type Digest struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Known возвращает true, если digest содержит реальный (не unknown) value.
func (d Digest) Known() bool {
	return d.Value != "" && d.Value != UnknownValue
}

// UnknownDigest строит unknown-digest (sha256/unknown).
func UnknownDigest() Digest {
	return Digest{Type: "sha256", Value: UnknownValue}
}

// Item — отдельный authority-bearing источник внутри manifest.
type Item struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Digest Digest `json:"digest"`
}

// Manifest — provenance manifest v1. Хранится inline в run.json (RunManifest)
// и сверяется с заново построенным manifest'ом при resume (drift detection).
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	GeneratedAt   time.Time `json:"generated_at"`
	Items         []Item    `json:"items"`
}

// New создаёт пустой manifest с согласованным schema_version.
func New(runID string, now time.Time) *Manifest {
	return &Manifest{SchemaVersion: SchemaVersion, RunID: runID, GeneratedAt: now}
}

// Add добавляет источник (kind+name) с заданным digest.
func (m *Manifest) Add(kind, name string, digest Digest) {
	if m == nil {
		return
	}
	m.Items = append(m.Items, Item{Kind: kind, Name: name, Digest: digest})
}

// AddJSON добавляет источник с canonical digest сериализации v. Если v не
// сериализуется, источник помечается unknown.
func (m *Manifest) AddJSON(kind, name string, v any) {
	digest, err := DigestJSON(v)
	if err != nil {
		m.AddUnknown(kind, name)
		return
	}
	m.Add(kind, name, digest)
}

// AddBytes добавляет источник с digest сырых байт.
func (m *Manifest) AddBytes(kind, name string, data []byte) {
	if len(data) == 0 {
		m.AddUnknown(kind, name)
		return
	}
	m.Add(kind, name, DigestBytes(data))
}

// AddUnknown добавляет источник, который невозможно разрешить (unknown).
func (m *Manifest) AddUnknown(kind, name string) {
	m.Add(kind, name, UnknownDigest())
}

// Find возвращает digest источника kind+name и флаг наличия.
func (m *Manifest) Find(kind, name string) (Digest, bool) {
	if m == nil {
		return Digest{}, false
	}
	for _, item := range m.Items {
		if item.Kind == kind && ((name == "" && item.Name == "") || (name != "" && item.Name == name)) {
			return item.Digest, true
		}
	}
	return Digest{}, false
}

// DigestBytes вычисляет sha256 digest байт.
func DigestBytes(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest{Type: "sha256", Value: hex.EncodeToString(sum[:])}
}

// DigestJSON вычисляет canonical JSON-представление v (json.Marshal, стабильный
// порядок ключей map) и его sha256 digest. Ошибка означает, что источник не
// сериализуется — вызывающий должен решить (обычно → unknown).
func DigestJSON(v any) (Digest, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Digest{}, err
	}
	return DigestBytes(data), nil
}

// CheckDrift сравнивает сохранённый (stored) и актуальный (live) manifest.
// Authority-bearing поле дрифтит только когда оба значения known и различны,
// либо когда live-источник отсутствует/новый в stored. Unknown никогда не
// дрифтит: мы не знаем, каким значение было, поэтому не утверждаем.
// Both nil — легаси run без provenance — не ошибка.
func CheckDrift(stored, live *Manifest) error {
	if stored == nil || live == nil {
		return nil
	}
	if stored.SchemaVersion != SchemaVersion {
		return fmt.Errorf("provenance drift: сохранённый manifest имеет неподдерживаемую schema_version %d (ожидается %d)",
			stored.SchemaVersion, SchemaVersion)
	}
	for _, liveItem := range live.Items {
		if !liveItem.Digest.Known() {
			continue
		}
		storedDigest, found := stored.Find(liveItem.Kind, liveItem.Name)
		if !found {
			return fmt.Errorf("provenance drift: %s %s отсутствует в сохранённом manifest",
				liveItem.Kind, description(liveItem.Name))
		}
		if storedDigest.Known() && storedDigest.Value != liveItem.Digest.Value {
			return fmt.Errorf("provenance drift: %s %s изменился (%s)",
				liveItem.Kind, description(liveItem.Name), liveItem.Name)
		}
	}
	for _, storedItem := range stored.Items {
		if !storedItem.Digest.Known() {
			continue
		}
		liveDigest, found := live.Find(storedItem.Kind, storedItem.Name)
		if !found || !liveDigest.Known() {
			return fmt.Errorf("provenance drift: %s %s пропал или стал unknown",
				storedItem.Kind, description(storedItem.Name))
		}
	}
	return nil
}

func description(name string) string {
	if name == "" {
		return "(глобальный)"
	}
	return name
}
