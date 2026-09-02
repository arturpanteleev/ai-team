package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitJSONStructured(t *testing.T) {
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	emitter = &Emitter{out: out, err: errBuf, mode: ModeJSON}

	Emit(Record{Level: "ok", Command: "run", Type: "run", Message: "завершён", Data: map[string]any{"run_id": "r-1"}, Exit: 0})

	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatalf("JSON mode должен писать record в stdout")
	}
	var rec Record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("не-валидный JSON: %v", err)
	}
	if rec.Level != "ok" || rec.Command != "run" || rec.Type != "run" || rec.Data["run_id"] != "r-1" {
		t.Fatalf("record неполный: %+v", rec)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("human ошиibka не должна писаться в err в JSON mode при ok")
	}
	if GetMode() != ModeJSON {
		t.Fatalf("mode не сохранился")
	}
}

func TestSetModeRoundtrip(t *testing.T) {
	SetMode(ModeQuiet)
	if GetMode() != ModeQuiet {
		t.Fatalf("quiet not set")
	}
	SetMode(ModeDefault)
	if GetMode() != ModeDefault {
		t.Fatalf("default not restored")
	}
}
