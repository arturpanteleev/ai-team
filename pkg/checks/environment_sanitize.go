package checks

import "strings"

// checkEnvironmentAllowlist names the only host variables an untrusted check
// command inherits. Everything else is dropped: an allowlist cannot miss a
// credential with an unexpected name (DATABASE_URL, KUBECONFIG, ...). HOME,
// TMPDIR, GOPATH and the Go caches are always reset by the sandbox itself.
var checkEnvironmentAllowlist = map[string]bool{
	"PATH": true, "LANG": true, "LANGUAGE": true, "TERM": true, "TZ": true,
	"USER": true, "LOGNAME": true, "SHELL": true, "CI": true, "NO_COLOR": true,
	// Go build settings that select a toolchain or target, not a source.
	"GOFLAGS": true, "GOTOOLCHAIN": true, "GOOS": true, "GOARCH": true,
	"GOARM": true, "GOAMD64": true, "GOEXPERIMENT": true, "GO111MODULE": true,
	"GOWORK": true, "GODEBUG": true, "GOMAXPROCS": true, "GOTRACEBACK": true,
	"CGO_ENABLED": true, "CC": true, "CXX": true,
}

func sanitizedCheckEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || !allowedCheckEnvironmentKey(key) || isSecretEnvironmentKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func allowedCheckEnvironmentKey(key string) bool {
	return checkEnvironmentAllowlist[key] || strings.HasPrefix(key, "LC_")
}

// isSecretEnvironmentKey is defense in depth behind the allowlist.
func isSecretEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTH", "COOKIE", "SESSION", "GH_", "GITHUB_", "OPENAI_"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
