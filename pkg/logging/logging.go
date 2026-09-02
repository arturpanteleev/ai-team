// Package logging предоставляет стабильный machine-readable вывод команд
// (OPS-6) и quiet/regex-режимы поверх stdout/err. Цель — чтобы результат
// команды был стабильно парсимым (JSON) для CI/агента, а не только
// человеческое форматирование.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Mode — режим вывода CLI.
type Mode int

const (
	// ModeDefault — человеко-читаемый (цвет по TTY), обычный.
	ModeDefault Mode = iota
	// ModeQuiet — подавить второстепенный вывод; только критичные/структурные.
	ModeQuiet
	// ModeJSON — стабильные JSON records на stdout, человеческие на stderr.
	ModeJSON
)

var (
	mu      sync.Mutex
	emitter = &Emitter{out: os.Stdout, err: os.Stderr, mode: ModeDefault}
)

// SetMode глобально меняет режим вывода CLI (вызывается из main до команды).
func SetMode(m Mode) {
	mu.Lock()
	defer mu.Unlock()
	emitter.mode = m
}

// GetMode возвращает текущий глобальный режим.
func GetMode() Mode {
	mu.Lock()
	defer mu.Unlock()
	return emitter.mode
}

// Record — стабильный machine-readable JSON record результата команды.
type Record struct {
	Level   string         `json:"level"`          // ok | error | warning | info
	Command string         `json:"cmd,omitempty"`  // имя подкоманды
	Type    string         `json:"type,omitempty"` // тип результата (напр. run / gate / verify / export)
	Message string         `json:"message"`        // человеко-читаемое описание
	Data    map[string]any `json:"data,omitempty"` // структурированные поля
	Exit    int            `json:"exit_code,omitempty"`
}

// Emitter пишет результаты в out (машиночитаемые) и err (человеческие).
type Emitter struct {
	out  io.Writer
	err  io.Writer
	mode Mode
}

// Emit печатает запись результата: в JSON mode — Стабильный JSON на stdout;
// в human mode — одной строкой на stderr (или stdout для ok).
func Emit(r Record) {
	mu.Lock()
	if emitter.mode == ModeJSON {
		data, _ := json.Marshal(r)
		fmt.Fprintln(emitter.out, string(data))
		mu.Unlock()
		return
	}
	mu.Unlock()
	// human
	prefix := "  "
	switch r.Level {
	case "ok":
		prefix = "✓ "
	case "error":
		prefix = "✗ "
	case "warning":
		prefix = "⚠ "
	case "info":
		prefix = "• "
	}
	if r.Level == "ok" && emitter.mode != ModeQuiet {
		fmt.Fprintf(emitter.out, "%s%s\n", prefix, r.Message)
	} else if r.Level != "ok" {
		fmt.Fprintf(emitter.err, "%s%s\n", prefix, r.Message)
	}
}

// Printf печатает человеко-читаемое сообщение (ранее fmt.Printf в stdout) в
// поток, зависящий от режима, чтобы в JSON-режиме stdout содержал только
// структурированные records, а в quiet — вообще ничего, кроме критичного
// (ошибки всегда идут в stderr через Emit/fatal). Маршрутизация:
//   - ModeJSON:  человеческий прогресс в stderr (stdout остаётся чистым JSON);
//   - ModeQuiet: второстепенный человеческий прогресс подавляется;
//   - ModeDefault: как раньше — в stdout.
func Printf(format string, args ...interface{}) {
	mu.Lock()
	mode := emitter.mode
	out := emitter.out
	err := emitter.err
	mu.Unlock()
	switch mode {
	case ModeJSON:
		fmt.Fprintf(err, format, args...)
	case ModeQuiet:
		// подавляем второстепенный человеческий прогресс
	default:
		fmt.Fprintf(out, format, args...)
	}
}

// Out returns the structured writer (stdout).
func Out() io.Writer { return os.Stdout }
