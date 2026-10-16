package pipeline

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var prNumberReference = regexp.MustCompile(`^[1-9][0-9]*$`)

// prReferenceFromOutput extracts a pull request reference from the content of
// one non-Markdown output of a `link_kind: pr` stage. Only two shapes are
// accepted, so that an extra output of an agent (a diff, a log, a patch) is
// never taken for the PR:
//
//   - the whole content is a PR URL (path ending in /pull/<n>) or a positive
//     PR number;
//   - the whole content is one JSON object whose `pr_url` (or `url`) field is
//     such a URL or number.
//
// The URL host must be github.com or the GitHub Enterprise host named by the
// GH_HOST environment variable (the same variable the gh CLI honours); the
// `url` field is accepted as a fallback when `pr_url` is absent.
//
// Anything else yields ok=false and the output is ignored.
func prReferenceFromOutput(data []byte) (string, bool) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "{") {
		var object map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
		if err := decoder.Decode(&object); err != nil || decoder.More() {
			return "", false
		}
		for _, key := range []string{"pr_url", "url"} {
			raw, exists := object[key]
			if !exists {
				continue
			}
			var value string
			if json.Unmarshal(raw, &value) == nil {
				return validPRReference(strings.TrimSpace(value))
			}
			return "", false
		}
		return "", false
	}
	return validPRReference(text)
}

func validPRReference(value string) (string, bool) {
	if prNumberReference.MatchString(value) {
		return value, true
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if !allowedPRHost(parsed.Hostname()) {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "pull" || !prNumberReference.MatchString(parts[len(parts)-1]) {
		return "", false
	}
	return value, true
}

// allowedPRHost limits PR URLs to github.com and the host configured through
// GH_HOST, so an arbitrary agent-produced URL is never stored as the PR.
func allowedPRHost(host string) bool {
	host = strings.ToLower(host)
	if host == "github.com" {
		return true
	}
	configured := strings.ToLower(strings.TrimSpace(os.Getenv("GH_HOST")))
	return configured != "" && host == configured
}
