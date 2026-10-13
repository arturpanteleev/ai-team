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
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

const jiraResponseLimit = 1 << 20

// issueKeyPattern matches a whole Jira issue key. It is applied only to a bare
// external-link field or to the last segment of a /browse/ URL on the
// configured Jira host, never searched for inside arbitrary text: substrings
// such as UTF-8, SHA-256 or CVE-2024 have the same shape as issue keys.
var issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,9}-[1-9][0-9]*$`)

// JiraAdapter changes Jira Cloud status and posts stage comments. It only
// sends data from ai-team to Jira and never polls or mutates ai-team state.
type JiraAdapter struct {
	settings config.JiraConfig
	token    string
	client   *http.Client
}

// NewJiraAdapter keeps the HTTP client injectable so callers and tests can
// control all network effects. A missing token is reported per event, which
// lets the web task service start and record integration errors without
// blocking task execution.
func NewJiraAdapter(settings config.JiraConfig, token string, client *http.Client) *JiraAdapter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JiraAdapter{settings: settings, token: token, client: client}
}

func (a *JiraAdapter) HandleTaskEvent(ctx context.Context, event TaskEvent) error {
	if a == nil || event.Type != "attempt_finished" {
		return nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(a.settings.BaseURL), "/")
	issueKey, err := issueKeyFromLinks(event.ExternalLinks, baseURL)
	if err != nil {
		return err
	}
	if issueKey == "" {
		return nil
	}
	if strings.TrimSpace(a.token) == "" {
		return fmt.Errorf("jira credential environment variable %s is empty", a.settings.TokenEnv)
	}
	if event.TaskID <= 0 {
		return errors.New("ai-team task link is unavailable")
	}
	if baseURL == "" {
		return errors.New("jira base URL is empty")
	}
	targetStatus := strings.TrimSpace(a.settings.StageStatuses[event.Stage])
	// attempt_finished is emitted for failed, rejected, infra-failed and
	// skipped attempts too. Only a successful stage may move the issue; every
	// outcome is still reported in the comment.
	if targetStatus != "" && attemptSucceeded(event.Data) {
		if err := a.transitionStatus(ctx, baseURL, issueKey, targetStatus); err != nil {
			return err
		}
	}
	return a.addComment(ctx, baseURL, issueKey, event, a.taskURL(event.TaskID))
}

// attemptSucceeded reports whether attempt_finished data describes a passed
// stage. Missing fields are treated as unsuccessful.
func attemptSucceeded(data map[string]any) bool {
	field := func(key string) string {
		value, _ := data[key].(string)
		return strings.TrimSpace(value)
	}
	return field("status") == workflow.StatusPassed &&
		field("outcome") == string(workflow.OutcomePassed) &&
		field("decision") != string(workflow.DecisionRejected)
}

// issueKeyFromLinks accepts a key only from a field that is exactly KEY-N or
// from a URL on the configured Jira host whose path is <base path>/browse/KEY-N.
func issueKeyFromLinks(links []string, baseURL string) (string, error) {
	var found string
	for _, link := range links {
		match := issueKeyFromLink(strings.TrimSpace(link), baseURL)
		if match == "" {
			continue
		}
		if found != "" && found != match {
			return "", errors.New("external links contain multiple Jira issue keys")
		}
		found = match
	}
	return found, nil
}

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

func (a *JiraAdapter) taskURL(taskID int64) string {
	base := strings.TrimRight(strings.TrimSpace(a.settings.WebBaseURL), "/")
	return fmt.Sprintf("%s/pipelines/%d", base, taskID)
}

type jiraIssueResponse struct {
	Fields struct {
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
	} `json:"fields"`
}

type jiraTransitionsResponse struct {
	Transitions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		To   struct {
			Name string `json:"name"`
		} `json:"to"`
	} `json:"transitions"`
}

func (a *JiraAdapter) transitionStatus(ctx context.Context, baseURL, issueKey, target string) error {
	issuePath := "/rest/api/3/issue/" + url.PathEscape(issueKey)
	var current jiraIssueResponse
	if err := a.requestJSON(ctx, http.MethodGet, baseURL+issuePath+"?fields=status", nil, &current); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(current.Fields.Status.Name), target) {
		return nil
	}
	var transitions jiraTransitionsResponse
	if err := a.requestJSON(ctx, http.MethodGet, baseURL+issuePath+"/transitions", nil, &transitions); err != nil {
		return err
	}
	transitionID := ""
	for _, candidate := range transitions.Transitions {
		if strings.EqualFold(strings.TrimSpace(candidate.To.Name), target) {
			transitionID = candidate.ID
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("jira has no available transition to status %q", target)
	}
	body := struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
	}{}
	body.Transition.ID = transitionID
	return a.requestJSON(ctx, http.MethodPost, baseURL+issuePath+"/transitions", body, nil)
}

// jiraCommentLookback is how many of the newest issue comments are checked
// for an attempt marker before posting. A replay happens right after the
// original comment, so it is among the newest ones.
const jiraCommentLookback = 50

func (a *JiraAdapter) addComment(ctx context.Context, baseURL, issueKey string, event TaskEvent, taskURL string) error {
	result := stageResult(event)
	blocks := []adfBlock{{
		Type: "paragraph",
		Content: []adfText{
			{Type: "text", Text: "ai-team task: " + taskURL},
		},
	}, {
		Type:    "paragraph",
		Content: []adfText{{Type: "text", Text: result}},
	}}
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/comment"
	marker := attemptMarker(event.AttemptID)
	if marker != "" {
		// The marker makes the comment idempotent per attempt: a replay after
		// a crash between Jira accepting the comment and the local cursor
		// commit finds it and does not post a second one.
		posted, err := a.hasRecentComment(ctx, baseURL+path, marker)
		if err != nil {
			return err
		}
		if posted {
			return nil
		}
		blocks = append(blocks, adfBlock{Type: "paragraph", Content: []adfText{{Type: "text", Text: marker}}})
	}
	body := struct {
		Body adfDocument `json:"body"`
	}{Body: adfDocument{Version: 1, Type: "doc", Content: blocks}}
	return a.requestJSON(ctx, http.MethodPost, baseURL+path, body, nil)
}

// attemptMarker is the text node that identifies the comment of one attempt.
func attemptMarker(attemptID string) string {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return ""
	}
	return "ai-team attempt: " + attemptID
}

type jiraCommentsResponse struct {
	Comments []struct {
		Body json.RawMessage `json:"body"`
	} `json:"comments"`
}

func (a *JiraAdapter) hasRecentComment(ctx context.Context, endpoint, marker string) (bool, error) {
	query := url.Values{"orderBy": {"-created"}, "maxResults": {fmt.Sprint(jiraCommentLookback)}}
	var comments jiraCommentsResponse
	if err := a.requestJSON(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, &comments); err != nil {
		return false, err
	}
	for _, comment := range comments.Comments {
		var body any
		if err := json.Unmarshal(comment.Body, &body); err != nil {
			continue
		}
		if adfHasText(body, marker) {
			return true, nil
		}
	}
	return false, nil
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

func stageResult(event TaskEvent) string {
	parts := []string{"Stage result: " + event.Stage}
	for _, key := range []string{"status", "outcome", "decision", "execution"} {
		if value, ok := event.Data[key]; ok && value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				parts = append(parts, key+"="+text)
			}
		}
	}
	return strings.Join(parts, "; ")
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
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create jira request: %w", err)
	}
	request.SetBasicAuth(a.settings.Email, a.token)
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
		return fmt.Errorf("jira %s %s returned HTTP %d", method, request.URL.EscapedPath(), response.StatusCode)
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
