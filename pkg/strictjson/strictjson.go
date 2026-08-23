// Package strictjson предоставляет единый строгий JSON-декод: ограничение
// размера тела, отказ от неизвестных полей и от нескольких JSON-документов.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Unmarshal декодирует ровно один JSON-документ из data в dst. Ввод не может
// быть пустым или превышать limit байт. Неизвестные поля и trailing
// JSON-документы приводят к ошибке.
func Unmarshal(data []byte, limit int64, dst any) error {
	if len(data) == 0 {
		return errors.New("пустой JSON ввод")
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("JSON размером %d байт превышает limit %d", len(data), limit)
	}
	return Decode(bytes.NewReader(data), dst)
}

// Decode декодирует ровно один JSON-документ из r в dst с запретом неизвестных
// полей и trailing JSON-документов. Ограничение размера остаётся за вызывающим
// (например, http.MaxBytesReader).
func Decode(r io.Reader, dst any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("невалидный JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON должен содержать один документ")
	}
	return nil
}
