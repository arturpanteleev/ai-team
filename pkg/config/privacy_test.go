package config

import (
	"testing"
)

func TestRedactionConfigValidate(t *testing.T) {
	ok := &RedactionConfig{Include: []string{"runs/**"}, Exclude: []string{"**/reports/**"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("валидный redaction: %v", err)
	}
	bad := &RedactionConfig{Include: []string{"../escape"}}
	if err := bad.Validate(); err == nil {
		t.Error("traversal-включение должно быть отвергнуто")
	}
}

func TestRedactionEffectiveFailClosedByDefault(t *testing.T) {
	if !(&RedactionConfig{}).EffectiveFailExportOnSecrets() {
		t.Error("по умолчанию экспорт должен быть fail-closed")
	}
	if (&RedactionConfig{DisableExportBlock: true}).EffectiveFailExportOnSecrets() {
		t.Error("явный disable_export_block должен снимать блок")
	}
	var nilCfg *RedactionConfig
	if !nilCfg.EffectiveFailExportOnSecrets() {
		t.Error("nil redaction → блок экспорта включён")
	}
}

func TestRetentionConfigValidateAndEffective(t *testing.T) {
	ok := &RetentionConfig{OlderThan: "720h", KeepLast: 5}
	if err := ok.Validate(); err != nil {
		t.Fatalf("валидный retention: %v", err)
	}
	bad := &RetentionConfig{OlderThan: "not-a-duration"}
	if err := bad.Validate(); err == nil {
		t.Error("невалидный older_than должен быть отвергнут")
	}

	var nilCfg *RetentionConfig
	if keep := nilCfg.EffectiveKeepLast(); keep != DefaultRetentionKeepLast {
		t.Fatalf("nil keep_last = %d", keep)
	}
	dur, err := nilCfg.EffectiveOlderThanDuration()
	if err != nil || dur.Hours() != 720 {
		t.Fatalf("nil older_than = %v (%v)", dur, err)
	}
}
