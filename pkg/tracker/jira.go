package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

const jiraResponseLimit = 1 << 20

var issueKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]{0,9}-[1-9][0-9]*\b`)

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
	issueKey, err := issueKeyFromLinks(event.ExternalLinks)
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
	baseURL := strings.TrimRight(strings.TrimSpace(a.settings.BaseURL), "/")
	if baseURL == "" {
		return errors.New("jira base URL is empty")
	}
	targetStatus := strings.TrimSpace(a.settings.StageStatuses[event.Stage])
	if targetStatus != "" {
		if err := a.transitionStatus(ctx, baseURL, issueKey, targetStatus); err != nil {
			return err
		}
	}
	return a.addComment(ctx, baseURL, issueKey, event, a.taskURL(event.TaskID))
}

func issueKeyFromLinks(links []string) (string, error) {
	var found string
	for _, link := range links {
		for _, match := range issueKeyPattern.FindAllString(link, -1) {
			if found != "" && found != match {
				return "", errors.New("external links contain multiple Jira issue keys")
			}
			found = match
		}
	}
	return found, nil
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

func (a *JiraAdapter) addComment(ctx context.Context, baseURL, issueKey string, event TaskEvent, taskURL string) error {
	result := stageResult(event)
	body := struct {
		Body adfDocument `json:"body"`
	}{Body: adfDocument{Version: 1, Type: "doc", Content: []adfBlock{{
		Type: "paragraph",
		Content: []adfText{
			{Type: "text", Text: "ai-team task: " + taskURL},
		},
	}, {
		Type:    "paragraph",
		Content: []adfText{{Type: "text", Text: result}},
	}}}}
	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/comment"
	return a.requestJSON(ctx, http.MethodPost, baseURL+path, body, nil)
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

// safeNetworkError avoids retaining URLs, which can contain deployment-specific
// paths and query values, while preserving the transport failure for logs.
func safeNetworkError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return fmt.Errorf("%s: %w", urlError.Op, urlError.Err)
	}
	return err
}
