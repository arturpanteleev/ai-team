package runtime

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Короткая запись из кап-буфера — это io.ErrShortWrite в io.MultiWriter, то
// есть падение успешного прогона агента из-за переполнения буфера разбора.
func TestCaptureBufferNeverReportsShortWrite(t *testing.T) {
	var capture captureBuffer
	var console bytes.Buffer
	writer := io.MultiWriter(&console, &capture)

	chunk := bytes.Repeat([]byte("a"), 7919) // не кратно ни лимиту, ни голове
	total := 0
	for total < 3*captureLimitBytes {
		n, err := writer.Write(chunk)
		if err != nil {
			t.Fatalf("запись %d B упала после %d B: %v", len(chunk), total, err)
		}
		if n != len(chunk) {
			t.Fatalf("короткая запись: n=%d, ожидалось %d", n, len(chunk))
		}
		total += n
	}
	if console.Len() != total {
		t.Fatalf("консоль получила %d B вместо %d", console.Len(), total)
	}
	if capture.Len() > captureLimitBytes {
		t.Fatalf("буфер вырос до %d B при лимите %d", capture.Len(), captureLimitBytes)
	}
}

func TestCaptureBufferKeepsWholeOutputWithinLimit(t *testing.T) {
	var capture captureBuffer
	payload := strings.Repeat("line\n", 1000)
	if _, err := io.WriteString(&capture, payload); err != nil {
		t.Fatal(err)
	}
	if capture.Dropped() != 0 {
		t.Fatalf("ничего не должно выбрасываться: %d", capture.Dropped())
	}
	if capture.Text() != payload {
		t.Fatal("вывод в пределах лимита обязан сохраняться целиком")
	}
}

func TestCaptureBufferKeepsHeadAndTail(t *testing.T) {
	var capture captureBuffer
	head := "AUTH FAILED: 401 unauthorized\n"
	tail := "{\"type\":\"turn.completed\"}\n"
	middle := strings.Repeat("{\"type\":\"item.completed\"}\n", 200000)

	for _, part := range []string{head, middle, tail} {
		if _, err := io.WriteString(&capture, part); err != nil {
			t.Fatal(err)
		}
	}
	if capture.Dropped() == 0 {
		t.Fatal("середина обязана быть выброшена на выводе сверх лимита")
	}
	text := capture.Text()
	if !strings.HasPrefix(text, head) {
		t.Fatalf("голова потеряна: %q", text[:min(len(text), 80)])
	}
	if !strings.HasSuffix(text, tail) {
		t.Fatalf("хвост потерян: %q", text[max(0, len(text)-80):])
	}
	if len(text) > captureLimitBytes {
		t.Fatalf("текст %d B превышает лимит %d", len(text), captureLimitBytes)
	}
}

// Склейка головы и хвоста не должна порождать «строку-химеру» из двух
// обрывков: codex считает любую невалидную JSONL-строку фатальной.
func TestCaptureBufferAlignsCutToLineBoundaries(t *testing.T) {
	var capture captureBuffer
	line := strings.Repeat("x", 99) + "\n"
	for written := 0; written < 4*captureLimitBytes; written += len(line) {
		if _, err := io.WriteString(&capture, line); err != nil {
			t.Fatal(err)
		}
	}
	for index, got := range strings.Split(strings.TrimSuffix(capture.Text(), "\n"), "\n") {
		if len(got) != 99 {
			t.Fatalf("строка %d склеена/обрезана: длина %d вместо 99", index, len(got))
		}
	}
}

func TestCaptureBufferSingleOversizedLineYieldsNothingParsable(t *testing.T) {
	var capture captureBuffer
	if _, err := io.WriteString(&capture, strings.Repeat("y", 3*captureLimitBytes)); err != nil {
		t.Fatal(err)
	}
	// Ни голова, ни хвост не являются валидной строкой: честнее отдать пусто
	// (и сообщить о пропуске), чем скормить парсеру обрывок.
	if text := capture.Text(); text != "" {
		t.Fatalf("обрывок единственной строки не должен попадать в разбор: %d B", len(text))
	}
	if capture.Dropped() == 0 {
		t.Fatal("выброшенные байты обязаны считаться")
	}
}
