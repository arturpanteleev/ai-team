package provenance

import (
	"strings"
	"testing"
	"time"
)

func digestValue(t *testing.T, m *Manifest, kind, name string) Digest {
	t.Helper()
	digest, found := m.Find(kind, name)
	if !found {
		t.Fatalf("в manifest отсутствует источник %s %q", kind, name)
	}
	return digest
}

func TestDigestBytesAndJSONDeterministic(t *testing.T) {
	a := DigestBytes([]byte("x"))
	b := DigestBytes([]byte("x"))
	if a != b || !a.Known() {
		t.Fatalf("DigestBytes детерминированность: %+v vs %+v", a, b)
	}
	// Canonical JSON: порядок ключей map в Go стабилен.
	ja, err := DigestJSON(map[string]int{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	jb, err := DigestJSON(map[string]int{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if ja != jb {
		t.Fatalf("DigestJSON не устойчив к порядку ключей: %+v vs %+v", ja, jb)
	}
	actor := struct {
		A int `json:"a"`
		B int `json:"b"`
	}{A: 1, B: 2}
	jc, err := DigestJSON(actor)
	if err != nil {
		t.Fatal(err)
	}
	if jc != ja {
		t.Fatalf("canonical digest struct-проекции не совпал с map: %+v vs %+v", jc, ja)
	}
}

func TestAddUnknownAndFind(t *testing.T) {
	m := New("run-1", time.Now())
	m.AddUnknown("base", "")
	digest := digestValue(t, m, "base", "")
	if digest.Known() || digest.Value != UnknownValue {
		t.Fatalf("expected unknown, got %+v", digest)
	}
	if _, found := m.Find("missing", ""); found {
		t.Fatalf("несуществующий источник найден")
	}
	if m.SchemaVersion != SchemaVersion || m.RunID != "run-1" {
		t.Fatalf("неверные базовые поля: %+v", m)
	}
}

func TestCheckDriftMatrix(t *testing.T) {
	now := time.Now()
	known := func(seed string) Digest { return DigestBytes([]byte(seed)) }

	t.Run("identical", func(t *testing.T) {
		build := func() *Manifest {
			m := New("run-1", now)
			m.Add("runtime", "", known("bin"))
			m.Add("prompt", "coder", known("p"))
			m.AddUnknown("candidate", "")
			return m
		}
		if err := CheckDrift(build(), build()); err != nil {
			t.Fatalf("идентичные manifest не должны дрифтить: %v", err)
		}
	})

	t.Run("legacy nil", func(t *testing.T) {
		live := New("run-1", now)
		live.Add("runtime", "", known("bin"))
		if err := CheckDrift(nil, live); err != nil {
			t.Fatalf("legacy run без сохранённого provenance не должен дрифтить: %v", err)
		}
	})

	t.Run("known change is drift", func(t *testing.T) {
		stored := New("run-1", now)
		stored.Add("runtime", "", known("bin-a"))
		live := New("run-1", now)
		live.Add("runtime", "", known("bin-b"))
		if err := CheckDrift(stored, live); err == nil ||
			!strings.Contains(err.Error(), "runtime (глобальный)") {
			t.Fatalf("ожидался drift runtime, got %v", err)
		}
	})

	t.Run("unknown never drifts", func(t *testing.T) {
		stored := New("run-1", now)
		stored.Add("candidate", "", UnknownDigest())
		live := New("run-1", now)
		live.Add("candidate", "", known("content"))
		if err := CheckDrift(stored, live); err != nil {
			t.Fatalf("unknown → known не должно считаться drift'ом: %v", err)
		}
		// В обратную сторону: stored known, live unknown — это дрейф (знание потерялось).
		stored2 := New("run-1", now)
		stored2.Add("prompt", "coder", known("p"))
		live2 := New("run-1", now)
		live2.Add("prompt", "coder", UnknownDigest())
		if err := CheckDrift(stored2, live2); err == nil ||
			!strings.Contains(err.Error(), "prompt coder пропал") {
			t.Fatalf("known → unknown должно считаться drift'ом, got %v", err)
		}
	})

	t.Run("live authority missing in stored", func(t *testing.T) {
		stored := New("run-1", now)
		stored.Add("runtime", "", known("bin"))
		live := New("run-1", now)
		live.Add("runtime", "", known("bin"))
		live.Add("prompt", "coder", known("p"))
		if err := CheckDrift(stored, live); err == nil ||
			!strings.Contains(err.Error(), "prompt coder") {
			t.Fatalf("ожидался drift по отсутствующему live-источнику, got %v", err)
		}
	})

	t.Run("stored authority disappeared from live", func(t *testing.T) {
		stored := New("run-1", now)
		stored.Add("runtime", "", known("bin"))
		stored.Add("prompt", "coder", known("p"))
		live := New("run-1", now)
		live.Add("runtime", "", known("bin"))
		if err := CheckDrift(stored, live); err == nil ||
			!strings.Contains(err.Error(), "prompt coder пропал") {
			t.Fatalf("ожидался drift по пропавшему источнику, got %v", err)
		}
	})
}
