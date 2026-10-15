package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/config"
)

// JiraName is the adapter name of Jira.
const JiraName = "jira"

// JiraAdapter is the Jira Tracker. It speaks REST API v3 (ADF comments) or v2
// (plain-text comments, also used by Jira Server/Data Center). Credentials are
// never logged and transport errors are reduced to their class.
type JiraAdapter struct {
	settings config.JiraConfig
	token    string
	client   *http.Client
}

// NewJiraAdapter keeps the HTTP client injectable so callers and tests can
// control all network effects. A missing token is reported per call, which
// lets the web server start and record integration errors without blocking
// task execution.
func NewJiraAdapter(settings config.JiraConfig, token string, client *http.Client) *JiraAdapter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JiraAdapter{settings: settings, token: token, client: client}
}

func openJira(cfg *config.TrackerConfig, deps Deps) (Tracker, error) {
	if cfg == nil || cfg.Jira == nil {
		return nil, errors.New("tracker.jira is not configured")
	}
	return NewJiraAdapter(*cfg.Jira, deps.Getenv(cfg.Jira.TokenEnv), deps.HTTPClient), nil
}

// Name implements Tracker.
func (a *JiraAdapter) Name() string { return JiraName }

// ValidateKey implements Tracker.
func (a *JiraAdapter) ValidateKey(key string) error {
	if !issueKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: expected PROJECT-123", ErrInvalidKey)
	}
	return nil
}

// KeyFromLink implements LinkParser.
func (a *JiraAdapter) KeyFromLink(link string) (string, bool) {
	key := issueKeyFromLink(strings.TrimSpace(link), a.baseURL())
	return key, key != ""
}

func (a *JiraAdapter) baseURL() string {
	return strings.TrimRight(strings.TrimSpace(a.settings.BaseURL), "/")
}

func (a *JiraAdapter) apiVersion() int {
	if a.settings.APIVersion == 2 {
		return 2
	}
	return 3
}

func (a *JiraAdapter) issuePath(key string) (string, error) {
	if err := a.ValidateKey(key); err != nil {
		return "", err
	}
	if a.baseURL() == "" {
		return "", errors.New("jira base URL is empty")
	}
	return fmt.Sprintf("%s/rest/api/%d/issue/%s", a.baseURL(), a.apiVersion(), url.PathEscape(key)), nil
}

type jiraIssueResponse struct {
	Fields struct {
		Summary string `json:"summary"`
		Status  struct {
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

// GetIssue implements Tracker.
func (a *JiraAdapter) GetIssue(ctx context.Context, key string) (Issue, error) {
	path, err := a.issuePath(key)
	if err != nil {
		return Issue{}, err
	}
	var issue jiraIssueResponse
	if err := a.requestJSON(ctx, http.MethodGet, path+"?fields=status,summary", nil, &issue); err != nil {
		return Issue{}, err
	}
	return Issue{Key: key, Summary: issue.Fields.Summary, Status: issue.Fields.Status.Name, URL: a.baseURL() + "/browse/" + key}, nil
}

// Transitions implements Tracker.
func (a *JiraAdapter) Transitions(ctx context.Context, key string) ([]Transition, error) {
	path, err := a.issuePath(key)
	if err != nil {
		return nil, err
	}
	var response jiraTransitionsResponse
	if err := a.requestJSON(ctx, http.MethodGet, path+"/transitions", nil, &response); err != nil {
		return nil, err
	}
	moves := make([]Transition, 0, len(response.Transitions))
	for _, candidate := range response.Transitions {
		moves = append(moves, Transition{ID: candidate.ID, Name: candidate.Name, To: candidate.To.Name})
	}
	return moves, nil
}

// Transition implements Tracker. The target is matched against the
// destination status first and then against the transition name.
func (a *JiraAdapter) Transition(ctx context.Context, key, target string) (bool, error) {
	target = strings.TrimSpace(target)
	issue, err := a.GetIssue(ctx, key)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(strings.TrimSpace(issue.Status), target) {
		return false, nil
	}
	moves, err := a.Transitions(ctx, key)
	if err != nil {
		return false, err
	}
	transitionID := ""
	for _, candidate := range moves {
		if strings.EqualFold(strings.TrimSpace(candidate.To), target) {
			transitionID = candidate.ID
			break
		}
	}
	if transitionID == "" {
		for _, candidate := range moves {
			if strings.EqualFold(strings.TrimSpace(candidate.Name), target) {
				transitionID = candidate.ID
				break
			}
		}
	}
	if transitionID == "" {
		return false, fmt.Errorf("%w %q", ErrNoTransition, target)
	}
	path, _ := a.issuePath(key)
	body := struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
	}{}
	body.Transition.ID = transitionID
	if err := a.requestJSON(ctx, http.MethodPost, path+"/transitions", body, nil); err != nil {
		return false, err
	}
	return true, nil
}

// jiraCommentLookback is how many of the newest issue comments are checked
// for the idempotency marker before posting. A replay or a manual retry
// happens soon after the original comment, so it is among the newest ones.
const jiraCommentLookback = 50

// CommentMarker is the text that identifies the comment with an idempotency
// key; it is the last paragraph (v3) or line (v2) of the comment.
func CommentMarker(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "ai-team event: " + key
}

// AddComment implements Tracker. Before posting it reads the newest comments
// and does not post a second comment with the same marker; when that check
// fails the comment is not posted.
func (a *JiraAdapter) AddComment(ctx context.Context, key string, comment Comment) (bool, error) {
	path, err := a.issuePath(key)
	if err != nil {
		return false, err
	}
	path += "/comment"
	marker := CommentMarker(comment.Key)
	if marker != "" {
		posted, err := a.hasRecentComment(ctx, path, marker)
		if err != nil {
			return false, err
		}
		if posted {
			return false, nil
		}
	}
	lines := append([]string(nil), comment.Lines...)
	if marker != "" {
		lines = append(lines, marker)
	}
	var body any
	if a.apiVersion() == 2 {
		body = struct {
			Body string `json:"body"`
		}{Body: strings.Join(lines, "\n")}
	} else {
		blocks := make([]adfBlock, 0, len(lines))
		for _, line := range lines {
			blocks = append(blocks, adfBlock{Type: "paragraph", Content: []adfText{{Type: "text", Text: line}}})
		}
		body = struct {
			Body adfDocument `json:"body"`
		}{Body: adfDocument{Version: 1, Type: "doc", Content: blocks}}
	}
	if err := a.requestJSON(ctx, http.MethodPost, path, body, nil); err != nil {
		return false, err
	}
	return true, nil
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
		if text, ok := body.(string); ok {
			for _, line := range strings.Split(text, "\n") {
				if strings.TrimSpace(line) == marker {
					return true, nil
				}
			}
			continue
		}
		if adfHasText(body, marker) {
			return true, nil
		}
	}
	return false, nil
}

type jiraSearchResponse struct {
	Issues []struct {
		Key       string `json:"key"`
		Changelog struct {
			Histories []struct {
				ID      string `json:"id"`
				Created string `json:"created"`
				Author  struct {
					Email string `json:"emailAddress"`
					Name  string `json:"displayName"`
				} `json:"author"`
				Items []struct {
					Field      string `json:"field"`
					FromString string `json:"fromString"`
					ToString   string `json:"toString"`
				} `json:"items"`
			} `json:"histories"`
		} `json:"changelog"`
	} `json:"issues"`
}

// Changes implements Tracker. The cursor is "<unix ms>:<history id>" of the
// last status change returned. An empty cursor starts at the current time and
// returns nothing. Jira's search filters by issue update time, so the query
// reaches back a minute before the cursor and changes at or before the cursor
// are dropped locally; the cursor therefore never repeats a change. Only the
// changelog page that Jira embeds in the search result is read.
func (a *JiraAdapter) Changes(ctx context.Context, query ChangeQuery) (ChangePage, error) {
	if query.Cursor == "" {
		return ChangePage{Cursor: formatJiraCursor(time.Now().UTC().UnixMilli(), 0)}, nil
	}
	afterMS, afterID, err := parseJiraCursor(query.Cursor)
	if err != nil {
		return ChangePage{}, err
	}
	page := ChangePage{Cursor: query.Cursor}
	if len(query.Keys) == 0 {
		return page, nil
	}
	for _, key := range query.Keys {
		if err := a.ValidateKey(key); err != nil {
			return ChangePage{}, err
		}
	}
	if a.baseURL() == "" {
		return ChangePage{}, errors.New("jira base URL is empty")
	}
	since := time.UnixMilli(afterMS).UTC().Add(-time.Minute).Format("2006/01/02 15:04")
	jql := fmt.Sprintf(`key in (%s) AND updated >= "%s" ORDER BY updated ASC`, strings.Join(query.Keys, ","), since)
	values := url.Values{"jql": {jql}, "fields": {"status"}, "expand": {"changelog"}, "maxResults": {"100"}}
	var response jiraSearchResponse
	endpoint := fmt.Sprintf("%s/rest/api/%d/search?%s", a.baseURL(), a.apiVersion(), values.Encode())
	if err := a.requestJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return ChangePage{}, err
	}
	type found struct {
		change Change
		ms, id int64
	}
	var all []found
	for _, issue := range response.Issues {
		for _, history := range issue.Changelog.Histories {
			created, err := parseJiraTime(history.Created)
			if err != nil {
				continue
			}
			id, _ := strconv.ParseInt(history.ID, 10, 64)
			ms := created.UnixMilli()
			if ms < afterMS || (ms == afterMS && id <= afterID) {
				continue
			}
			for _, item := range history.Items {
				if item.Field != "status" {
					continue
				}
				origin := OriginExternal
				if email := strings.TrimSpace(a.settings.Email); email != "" && strings.EqualFold(email, history.Author.Email) {
					origin = OriginAITeam
				}
				author := history.Author.Name
				if author == "" {
					author = history.Author.Email
				}
				all = append(all, found{ms: ms, id: id, change: Change{
					ID: history.ID, IssueKey: issue.Key, From: item.FromString, To: item.ToString,
					At: created.UTC(), Author: author, Origin: origin,
				}})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ms != all[j].ms {
			return all[i].ms < all[j].ms
		}
		return all[i].id < all[j].id
	})
	for _, item := range all {
		if query.Limit > 0 && len(page.Changes) >= query.Limit {
			break
		}
		page.Changes = append(page.Changes, item.change)
		page.Cursor = formatJiraCursor(item.ms, item.id)
	}
	return page, nil
}

func formatJiraCursor(ms, id int64) string { return fmt.Sprintf("%d:%d", ms, id) }

func parseJiraCursor(cursor string) (int64, int64, error) {
	left, right, ok := strings.Cut(cursor, ":")
	ms, err1 := strconv.ParseInt(left, 10, 64)
	id, err2 := strconv.ParseInt(right, 10, 64)
	if !ok || err1 != nil || err2 != nil || ms < 0 || id < 0 {
		return 0, 0, errors.New("invalid tracker change cursor")
	}
	return ms, id, nil
}

// parseJiraTime reads Jira's timestamp, e.g. 2026-10-15T12:00:00.000+0000.
func parseJiraTime(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339Nano, "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("unparsable jira timestamp")
}
