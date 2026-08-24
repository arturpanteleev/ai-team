package strictjson

import (
	"strings"
	"testing"
)

type payload struct {
	Name string `json:"name"`
}

func TestUnmarshalValidDocument(t *testing.T) {
	var value payload
	if err := Unmarshal([]byte(`{"name":"ok"}`), 64, &value); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if value.Name != "ok" {
		t.Fatalf("неожиданное значение: %+v", value)
	}
}

func TestUnmarshalUnknownField(t *testing.T) {
	var value payload
	if err := Unmarshal([]byte(`{"name":"ok","extra":1}`), 64, &value); err == nil {
		t.Fatal("ожидалась ошибка на неизвестном поле")
	}
}

func TestUnmarshalTrailingDocument(t *testing.T) {
	var value payload
	data := `{"name":"ok"} {"name":"again"}`
	if err := Unmarshal([]byte(data), int64(len(data)), &value); err == nil {
		t.Fatal("ожидалась ошибка на второй JSON-документ")
	}
}

func TestUnmarshalOversizedBody(t *testing.T) {
	var value payload
	if err := Unmarshal([]byte(`{"name":"ok"}`), 4, &value); err == nil {
		t.Fatal("ожидалась ошибка на превышение limit")
	}
}

func TestUnmarshalEmptyInput(t *testing.T) {
	var value payload
	if err := Unmarshal(nil, 64, &value); err == nil {
		t.Fatal("ожидалась ошибка на пустой ввод")
	}
	if err := Unmarshal([]byte{}, 64, &value); err == nil {
		t.Fatal("ожидалась ошибка на пустой ввод")
	}
}

func TestDecodeRejectsTrailingGarbage(t *testing.T) {
	var value payload
	if err := Decode(strings.NewReader(`{"name":"ok"} garbage`), &value); err == nil {
		t.Fatal("ожидалась ошибка на мусор после JSON-документа")
	}
}
