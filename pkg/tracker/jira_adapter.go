package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	selfMu    sync.Mutex
	selfKnown bool
	self      jiraUser

	// changelogFallbacks counts issues whose full changelog could not be read
	// (an old Data Center without /changelog) and whose embedded part was used.
	changelogFallbacks atomic.Int64
}

// ChangelogFallbacks reports how many times the adapter had to use the
// truncated changelog embedded in a search result because the server has no
// /issue/{key}/changelog endpoint.
func (a *JiraAdapter) ChangelogFallbacks() int64 { return a.changelogFallbacks.Load() }

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

// Comments are read page by page, newest first, until the marker is found or
// the issue has no more comments. The page limit only protects against an
// endless loop; a card with more comments than that is reported as an error
// and nothing is posted, because a duplicate is worse than a delayed comment.
const (
	jiraCommentPageSize = 100
	jiraCommentMaxPages = 50
)

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
	Total      int `json:"total"`
	MaxResults int `json:"maxResults"`
	Comments   []struct {
		Body json.RawMessage `json:"body"`
	} `json:"comments"`
}

func (a *JiraAdapter) hasRecentComment(ctx context.Context, endpoint, marker string) (bool, error) {
	startAt := 0
	for page := 0; page < jiraCommentMaxPages; page++ {
		query := url.Values{"orderBy": {"-created"}, "startAt": {strconv.Itoa(startAt)}, "maxResults": {strconv.Itoa(jiraCommentPageSize)}}
		var comments jiraCommentsResponse
		if err := a.requestJSON(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, &comments); err != nil {
			return false, err
		}
		for _, comment := range comments.Comments {
			if jiraCommentHasMarker(comment.Body, marker) {
				return true, nil
			}
		}
		startAt += len(comments.Comments)
		if len(comments.Comments) == 0 {
			return false, nil
		}
		if comments.Total > 0 {
			if startAt >= comments.Total {
				return false, nil
			}
			continue
		}
		// total is missing: only a short page proves the end. A full page
		// (the size Jira actually applied) means there may be more.
		pageSize := comments.MaxResults
		if pageSize <= 0 {
			pageSize = jiraCommentPageSize
		}
		if len(comments.Comments) < pageSize {
			return false, nil
		}
	}
	return false, errors.New("jira issue has too many comments to check the idempotency marker")
}

func jiraCommentHasMarker(raw json.RawMessage, marker string) bool {
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	if text, ok := body.(string); ok {
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == marker {
				return true
			}
		}
		return false
	}
	return adfHasText(body, marker)
}

// jiraUser identifies the account behind the configured credentials.
type jiraUser struct {
	AccountID string `json:"accountId"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Email     string `json:"emailAddress"`
}

func (u jiraUser) matches(author jiraUser) bool {
	same := func(a, b string) bool { return a != "" && strings.EqualFold(a, b) }
	return same(u.AccountID, author.AccountID) || same(u.Name, author.Name) || same(u.Key, author.Key) || same(u.Email, author.Email)
}

// authenticatedUser asks Jira who the credentials belong to (/myself), which
// works for API tokens and personal access tokens alike and does not rely on
// jira.email matching the profile e-mail (Cloud often hides e-mail). The
// answer is cached. When it cannot be read there is no safe fallback (Jira
// Cloud hides e-mail, so the configured address would not match ai-team's own
// transitions and they would be read back as external requests), so the error
// is returned and the caller must not classify any change.
func (a *JiraAdapter) authenticatedUser(ctx context.Context) (jiraUser, error) {
	a.selfMu.Lock()
	defer a.selfMu.Unlock()
	if a.selfKnown {
		return a.self, nil
	}
	var me jiraUser
	endpoint := fmt.Sprintf("%s/rest/api/%d/myself", a.baseURL(), a.apiVersion())
	if err := a.requestJSON(ctx, http.MethodGet, endpoint, nil, &me); err != nil {
		return jiraUser{}, fmt.Errorf("jira account behind the credentials could not be determined: %w", err)
	}
	if me.Email == "" {
		me.Email = strings.TrimSpace(a.settings.Email)
	}
	if me.AccountID == "" && me.Name == "" && me.Key == "" && me.Email == "" {
		return jiraUser{}, errors.New("jira account behind the credentials could not be determined: empty /myself answer")
	}
	a.self, a.selfKnown = me, true
	return me, nil
}

type jiraHistory struct {
	ID      string `json:"id"`
	Created string `json:"created"`
	Author  struct {
		AccountID string `json:"accountId"`
		Login     string `json:"name"`
		Key       string `json:"key"`
		Email     string `json:"emailAddress"`
		Name      string `json:"displayName"`
	} `json:"author"`
	Items []struct {
		Field      string `json:"field"`
		FromString string `json:"fromString"`
		ToString   string `json:"toString"`
	} `json:"items"`
}

type jiraSearchIssue struct {
	Key       string `json:"key"`
	Changelog struct {
		Total      int           `json:"total"`
		MaxResults int           `json:"maxResults"`
		Histories  []jiraHistory `json:"histories"`
	} `json:"changelog"`
}

type jiraSearchResponse struct {
	Issues        []jiraSearchIssue `json:"issues"`
	NextPageToken string            `json:"nextPageToken"`
	IsLast        *bool             `json:"isLast"`
	StartAt       int               `json:"startAt"`
	Total         int               `json:"total"`
}

// jiraChangelogPage is one page of GET /issue/{key}/changelog.
type jiraChangelogPage struct {
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
	IsLast     *bool         `json:"isLast"`
	Values     []jiraHistory `json:"values"`
}

// Hard stops against a server that never ends a page chain: Changes fails
// instead of returning a truncated list, because the cursor would then skip
// the unread changes for good.
const (
	jiraSearchPageSize = 100
	jiraMaxPages       = 200
)

// Changes implements Tracker. The cursor is "<unix ms>:<history id>" of the
// last status change returned. An empty cursor starts at the current time and
// returns nothing. Jira's search filters by issue update time, so the query
// reaches back a minute before the cursor and changes at or before the cursor
// are dropped locally; the cursor therefore never repeats a change. A page
// is cut on a history-record boundary, so it may exceed Limit by the extra
// status items of the last record. Every page of the search result is read
// (nextPageToken on /search/jql, startAt on /search), and when the changelog
// embedded in an issue is cut short (Jira embeds only the oldest histories)
// the whole changelog is read from /issue/{key}/changelog. A change is
// AITeam-origin when its author is the account behind the credentials
// (/myself), not when jira.email happens to equal a profile e-mail.
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
	// The own account must be known before any change is classified: a wrong
	// guess would turn ai-team's own transitions into external requests. The
	// error leaves the cursor where it was.
	self, err := a.authenticatedUser(ctx)
	if err != nil {
		return ChangePage{}, err
	}
	// JQL date literals have no time zone (Jira reads them in the user's
	// zone), so the window is a relative offset in minutes, which does not
	// depend on any zone: from a minute before the cursor until now.
	window := int(math.Ceil(time.Since(time.UnixMilli(afterMS).Add(-time.Minute)).Minutes())) + 1
	if window < 1 {
		window = 1
	}
	type found struct {
		change Change
		ms, id int64
	}
	var all []found
	// Keys go to Jira in batches so the JQL and URL stay short; the results
	// are merged and ordered below, so the cursor is shared by all batches.
	for start := 0; start < len(query.Keys); start += jiraChangeKeyBatch {
		end := min(start+jiraChangeKeyBatch, len(query.Keys))
		response, err := a.searchChanges(ctx, query.Keys[start:end], window)
		if err != nil {
			return ChangePage{}, err
		}
		for _, issue := range response.Issues {
			histories, warning, err := a.issueHistories(ctx, issue)
			if err != nil {
				return ChangePage{}, err
			}
			if warning != "" && len(page.Warnings) < 20 {
				page.Warnings = append(page.Warnings, warning)
			}
			for _, history := range histories {
				created, err := parseJiraTime(history.Created)
				if err != nil {
					continue
				}
				id, _ := strconv.ParseInt(history.ID, 10, 64)
				ms := created.UnixMilli()
				if ms < afterMS || (ms == afterMS && id <= afterID) {
					continue
				}
				statusItems := 0
				for _, item := range history.Items {
					if item.Field != "status" {
						continue
					}
					// One history record may carry several status items. The
					// first keeps the history ID; the others get a suffix, so
					// each change has its own idempotency key.
					changeID := history.ID
					if statusItems > 0 {
						changeID = fmt.Sprintf("%s.%d", history.ID, statusItems)
					}
					statusItems++
					origin := OriginExternal
					if self.matches(jiraUser{AccountID: history.Author.AccountID, Name: history.Author.Login, Key: history.Author.Key, Email: history.Author.Email}) {
						origin = OriginAITeam
					}
					author := history.Author.Name
					if author == "" {
						author = history.Author.Email
					}
					all = append(all, found{ms: ms, id: id, change: Change{
						ID: changeID, IssueKey: issue.Key, From: item.FromString, To: item.ToString,
						At: created.UTC(), Author: author, Origin: origin,
					}})
				}
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ms != all[j].ms {
			return all[i].ms < all[j].ms
		}
		return all[i].id < all[j].id
	})
	for i, item := range all {
		// The cursor is (time, history ID), so a page is cut only on a record
		// boundary: the items of one history record are never split between
		// pages, or the cursor would skip the rest of the record.
		if query.Limit > 0 && len(page.Changes) >= query.Limit && (all[i-1].ms != item.ms || all[i-1].id != item.id) {
			break
		}
		page.Changes = append(page.Changes, item.change)
		page.Cursor = formatJiraCursor(item.ms, item.id)
	}
	return page, nil
}

// jiraChangeKeyBatch is how many issue keys one change query carries.
const jiraChangeKeyBatch = 50

// searchChanges reads the status changelogs of one batch of issues, following
// every page of the search result.
func (a *JiraAdapter) searchChanges(ctx context.Context, keys []string, window int) (jiraSearchResponse, error) {
	// The order is by key, not by update time: the key set is fixed by the
	// query, so an issue updated between two pages cannot move to another page
	// and be skipped by startAt paging (Changes sorts the changes itself).
	jql := fmt.Sprintf(`key in (%s) AND updated >= "-%dm" ORDER BY key ASC`, strings.Join(keys, ","), window)
	// /search/jql replaces the removed /search on Jira Cloud; Jira Server and
	// Data Center only know /search, so a 404 on the first page falls back to
	// it. The two differ in paging: nextPageToken against startAt.
	legacy := false
	var merged jiraSearchResponse
	token, startAt := "", 0
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page >= jiraMaxPages {
			return jiraSearchResponse{}, errors.New("jira search result has too many pages")
		}
		values := url.Values{"jql": {jql}, "fields": {"status"}, "expand": {"changelog"}, "maxResults": {strconv.Itoa(jiraSearchPageSize)}}
		path := "search/jql"
		if legacy {
			path = "search"
			values.Set("startAt", strconv.Itoa(startAt))
		} else if token != "" {
			values.Set("nextPageToken", token)
		}
		endpoint := fmt.Sprintf("%s/rest/api/%d/%s?%s", a.baseURL(), a.apiVersion(), path, values.Encode())
		var response jiraSearchResponse
		if err := a.requestJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			if page == 0 && !legacy && errors.Is(err, ErrNotFound) {
				legacy = true
				page = -1
				continue
			}
			return jiraSearchResponse{}, err
		}
		for _, issue := range response.Issues {
			if !seen[issue.Key] {
				seen[issue.Key] = true
				merged.Issues = append(merged.Issues, issue)
			}
		}
		if legacy {
			startAt += len(response.Issues)
			if len(response.Issues) == 0 || startAt >= response.Total {
				return merged, nil
			}
			continue
		}
		if response.NextPageToken == "" || (response.IsLast != nil && *response.IsLast) {
			return merged, nil
		}
		token = response.NextPageToken
	}
}

// changelogCut reports whether the changelog embedded in a search result may
// be incomplete. The total is the main signal; when the server omits it (zero
// with histories present), a full page of histories (the reported maxResults,
// or the page size) is treated as possibly cut.
func changelogCut(embedded int, total, maxResults int) bool {
	if total > embedded {
		return true
	}
	if total > 0 || embedded == 0 {
		return false
	}
	limit := maxResults
	if limit <= 0 || limit > jiraSearchPageSize {
		limit = jiraSearchPageSize
	}
	return embedded >= limit
}

// issueHistories returns the histories embedded in a search result, or, when
// Jira may have cut the embedded changelog short, the full changelog of the
// issue. A server without /changelog (old Data Center) answers 404: the
// embedded part is used, the fallback is counted and reported as a warning, so
// polling keeps working instead of failing forever.
func (a *JiraAdapter) issueHistories(ctx context.Context, issue jiraSearchIssue) ([]jiraHistory, string, error) {
	embedded := issue.Changelog.Histories
	if !changelogCut(len(embedded), issue.Changelog.Total, issue.Changelog.MaxResults) {
		return embedded, "", nil
	}
	path, err := a.issuePath(issue.Key)
	if err != nil {
		return nil, "", err
	}
	var all []jiraHistory
	for page := 0; page < jiraMaxPages; page++ {
		endpoint := fmt.Sprintf("%s/changelog?startAt=%d&maxResults=%d", path, len(all), jiraSearchPageSize)
		var response jiraChangelogPage
		if err := a.requestJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			if errors.Is(err, ErrNotFound) && page == 0 {
				a.changelogFallbacks.Add(1)
				return embedded, "jira: " + issue.Key + ": /changelog is not available, only the changelog embedded in the search result was read; later status changes may be missed", nil
			}
			return nil, "", err
		}
		all = append(all, response.Values...)
		pageLimit := response.MaxResults
		if pageLimit <= 0 || pageLimit > jiraSearchPageSize {
			pageLimit = jiraSearchPageSize
		}
		switch {
		case len(response.Values) == 0:
			return all, "", nil
		case response.IsLast != nil:
			if *response.IsLast {
				return all, "", nil
			}
		case response.Total > 0:
			if len(all) >= response.Total {
				return all, "", nil
			}
		case len(response.Values) < pageLimit:
			// No total and no isLast: a short page is the last one.
			return all, "", nil
		}
	}
	return nil, "", errors.New("jira changelog has too many pages")
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
