package workflow

import (
	"regexp"
	"strings"
)

var featurePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// FeatureRule — человекочитаемое правило имени feature для сообщений об ошибках.
// Должно соответствовать ValidFeature.
const FeatureRule = "латиница, цифры, точка, подчёркивание и дефис; первый символ — латиница или цифра; до 64 символов; без «..», без точки в конце и без суффикса «.lock»"

func ValidFeature(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.Contains(name, "..") && !strings.HasSuffix(name, ".") &&
		!strings.HasSuffix(name, ".lock") && featurePattern.MatchString(name)
}
