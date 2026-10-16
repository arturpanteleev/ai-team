package tracker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

const jiraResponseLimit = 1 << 20

// issueKeyPattern matches a whole Jira issue key. It is applied only to a bare
// external-link field or to the last segment of a /browse/ URL on the
// configured Jira host, never searched for inside arbitrary text: substrings
// such as UTF-8, SHA-256 or CVE-2024 have the same shape as issue keys.
var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}-[1-9][0-9]*$`)

func issueKeyFromLink(link, baseURL string) string {
	if issueKeyPattern.MatchString(link) {
		return link
	}
	if baseURL == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return ""
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if !sameHost(parsed, base) {
		return ""
	}
	prefix := strings.TrimRight(base.Path, "/") + "/browse/"
	key, ok := strings.CutPrefix(strings.TrimRight(parsed.Path, "/"), prefix)
	if !ok {
		return ""
	}
	// Jira resolves /browse/app-12 to APP-12. Case is folded only inside a
	// browse URL on the configured host; a bare lowercase field such as
	// "utf-8" stays unrecognized.
	key = strings.ToUpper(key)
	if !issueKeyPattern.MatchString(key) {
		return ""
	}
	return key
}

// sameHost compares host names case-insensitively and treats a port equal to
// the scheme's default as omitted, so https://jira.example and
// https://jira.example:443 are the same Jira while :8443 is not.
func sameHost(link, base *url.URL) bool {
	return strings.EqualFold(link.Hostname(), base.Hostname()) &&
		explicitPort(link) == explicitPort(base)
}

func explicitPort(u *url.URL) string {
	port := u.Port()
	switch {
	case port == "80" && strings.EqualFold(u.Scheme, "http"),
		port == "443" && strings.EqualFold(u.Scheme, "https"):
		return ""
	}
	return port
}

// adfHasText reports whether an ADF tree has a text node equal to want.
func adfHasText(node any, want string) bool {
	switch value := node.(type) {
	case map[string]any:
		if text, ok := value["text"].(string); ok && strings.TrimSpace(text) == want {
			return true
		}
		return adfHasText(value["content"], want)
	case []any:
		for _, child := range value {
			if adfHasText(child, want) {
				return true
			}
		}
	}
	return false
}

type adfDocument struct {
	Version int        `json:"version"`
	Type    string     `json:"type"`
	Content []adfBlock `json:"content"`
}

type adfBlock struct {
	Type    string    `json:"type"`
	Content []adfText `json:"content"`
}

type adfText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (a *JiraAdapter) requestJSON(ctx context.Context, method, endpoint string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode jira request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	if strings.TrimSpace(a.token) == "" {
		return fmt.Errorf("jira credential environment variable %s is empty", a.settings.TokenEnv)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create jira request: %w", err)
	}
	if a.settings.Auth == config.JiraAuthBearer {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.token))
	} else {
		request.SetBasicAuth(a.settings.Email, a.token)
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("jira %s request failed: %w", method, safeNetworkError(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err := fmt.Errorf("jira %s %s returned HTTP %d", method, request.URL.EscapedPath(), response.StatusCode)
		switch response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", err, ErrNotFound)
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %w", err, ErrForbidden)
		}
		return err
	}
	if responseBody == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, jiraResponseLimit))
		return nil
	}
	limited := io.LimitReader(response.Body, jiraResponseLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read jira response: %w", safeNetworkError(err))
	}
	if len(data) > jiraResponseLimit {
		return errors.New("jira response exceeded size limit")
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("decode jira response: %w", err)
	}
	return nil
}

// safeNetworkError reduces a transport failure to its class. The text of a
// transport error is never kept: *url.Error prints the request URL, and a
// proxy or custom transport may echo the URL or the Authorization header,
// while the result is persisted as an integration_error visible in the UI.
func safeNetworkError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("request timed out")
	case errors.Is(err, context.Canceled):
		return errors.New("request canceled")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("request timed out")
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return errors.New("TLS verification failed")
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op != "" {
		return errors.New("connection failed (" + opErr.Op + ")")
	}
	return errors.New("transport error")
}
