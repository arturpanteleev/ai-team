package checks

import "strings"

func sanitizedCheckEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || isSecretEnvironmentKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func isSecretEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTH", "COOKIE", "SESSION", "GH_", "GITHUB_", "OPENAI_"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
