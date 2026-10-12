package cicd

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
	"strconv"
	"strings"
	"time"
)

const (
	teamCityResponseLimit = 1 << 20
	maxTeamCityRedirects  = 5
)

var errForeignRedirect = errors.New("teamcity redirect to another host or scheme is not allowed")

var teamCityBuildTypePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,225}$`)

// ValidTeamCityBuildType accepts a TeamCity build configuration external ID.
func ValidTeamCityBuildType(value string) bool { return teamCityBuildTypePattern.MatchString(value) }

// TeamCitySettings selects the TeamCity server and deploy build configuration.
type TeamCitySettings struct {
	BaseURL   string
	BuildType string
}

// TeamCity triggers and observes builds through the TeamCity REST API with a
// bearer access token. The token is read by the caller from the controller's
// environment and is never included in errors.
type TeamCity struct {
	base      *url.URL
	buildType string
	token     string
	client    *http.Client
}

// NewTeamCity validates the server URL and wraps client with a redirect policy
// that only follows HTTPS redirects on the configured host.
func NewTeamCity(settings TeamCitySettings, token string, client *http.Client) (*TeamCity, error) {
	base, err := ValidateHTTPSBaseURL(settings.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("teamcity base_url: %w", err)
	}
	if !ValidTeamCityBuildType(settings.BuildType) {
		return nil, errors.New("teamcity build_type must be a build configuration ID")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("teamcity access token is empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	wrapped := *client
	wrapped.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxTeamCityRedirects {
			return errors.New("teamcity redirect limit exceeded")
		}
		if request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, base.Host) {
			return errForeignRedirect
		}
		return nil
	}
	return &TeamCity{base: base, buildType: settings.BuildType, token: token, client: &wrapped}, nil
}

// ValidateHTTPSBaseURL accepts an absolute HTTPS URL without credentials,
// query or fragment.
func ValidateHTTPSBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("must be an absolute https URL without credentials, query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

func (t *TeamCity) Name() string { return "teamcity" }

type teamCityBuild struct {
	ID           int64  `json:"id"`
	BuildTypeID  string `json:"buildTypeId"`
	State        string `json:"state"`
	Status       string `json:"status"`
	StatusText   string `json:"statusText"`
	WebURL       string `json:"webUrl"`
	CanceledInfo *struct {
		Text string `json:"text"`
	} `json:"canceledInfo"`
	Revisions *struct {
		Revision []struct {
			Version string `json:"version"`
		} `json:"revision"`
	} `json:"revisions"`
}

type teamCityTriggerBody struct {
	BuildType  teamCityBuildType  `json:"buildType"`
	BranchName string             `json:"branchName"`
	Revisions  teamCityRevisions  `json:"revisions"`
	Properties teamCityProperties `json:"properties"`
}

type teamCityBuildType struct {
	ID string `json:"id"`
}

type teamCityRevisions struct {
	FailOnMissingRevisions bool               `json:"failOnMissingRevisions"`
	Revision               []teamCityRevision `json:"revision"`
}

type teamCityRevision struct {
	Version       string `json:"version"`
	VCSBranchName string `json:"vcsBranchName"`
}

type teamCityProperties struct {
	Property []teamCityProperty `json:"property"`
}

type teamCityProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Trigger queues the configured build type pinned to the exact commit.
func (t *TeamCity) Trigger(ctx context.Context, request TriggerRequest) (Build, error) {
	if !ValidCommitSHA(request.CommitSHA) || !ValidBranch(request.Branch) {
		return Build{}, fmt.Errorf("%w: teamcity trigger requires a full commit SHA and a valid branch", ErrDeployRejected)
	}
	body := teamCityTriggerBody{
		BuildType:  teamCityBuildType{ID: t.buildType},
		BranchName: request.Branch,
		Revisions: teamCityRevisions{FailOnMissingRevisions: true, Revision: []teamCityRevision{{
			Version: request.CommitSHA, VCSBranchName: "refs/heads/" + request.Branch,
		}}},
		Properties: teamCityProperties{Property: []teamCityProperty{{Name: "env.AI_TEAM_DEPLOY_COMMIT", Value: request.CommitSHA}}},
	}
	var response teamCityBuild
	if err := t.requestJSON(ctx, http.MethodPost, "/app/rest/buildQueue", body, &response); err != nil {
		return Build{}, err
	}
	return t.convert(response)
}

// Build reads the current state of one build (queued, running or finished).
func (t *TeamCity) Build(ctx context.Context, id string) (Build, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return Build{}, errors.New("teamcity build id must be numeric")
	}
	var response teamCityBuild
	path := "/app/rest/builds/id:" + id
	if err := t.requestJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return Build{}, err
	}
	return t.convert(response)
}

// Cancel stops a queued or running build without re-adding it to the queue.
func (t *TeamCity) Cancel(ctx context.Context, build Build, reason string) error {
	if _, err := strconv.ParseInt(build.ID, 10, 64); err != nil {
		return errors.New("teamcity build id must be numeric")
	}
	path := "/app/rest/builds/id:" + build.ID
	if build.State == StateQueued {
		path = "/app/rest/buildQueue/id:" + build.ID
	}
	body := struct {
		Comment        string `json:"comment"`
		ReaddIntoQueue bool   `json:"readdIntoQueue"`
	}{Comment: reason}
	return t.requestJSON(ctx, http.MethodPost, path, body, nil)
}

func (t *TeamCity) convert(response teamCityBuild) (Build, error) {
	if response.ID <= 0 {
		return Build{}, errors.New("teamcity response has no build id")
	}
	build := Build{ID: strconv.FormatInt(response.ID, 10), BuildType: response.BuildTypeID, StatusText: limitText(response.StatusText, 512)}
	// A build ID from another build configuration (for example a tampered
	// receipt) must never stand in for the configured deploy build.
	if response.BuildTypeID != t.buildType {
		return Build{}, fmt.Errorf("%w: teamcity build %s belongs to another build configuration", ErrDeployRejected, build.ID)
	}
	switch strings.ToLower(response.State) {
	case "queued":
		build.State = StateQueued
	case "running":
		build.State = StateRunning
	case "finished":
		build.State = StateFinished
	default:
		return Build{}, fmt.Errorf("teamcity build %s has unknown state", build.ID)
	}
	switch {
	case build.State != StateFinished:
		build.Status = ""
	case response.CanceledInfo != nil:
		build.Status = StatusCanceled
	case strings.EqualFold(response.Status, "SUCCESS"):
		build.Status = StatusSuccess
	case strings.EqualFold(response.Status, "FAILURE"), strings.EqualFold(response.Status, "ERROR"):
		build.Status = StatusFailure
	default:
		build.Status = StatusUnknown
	}
	if response.Revisions != nil && len(response.Revisions.Revision) > 0 {
		revision := strings.ToLower(strings.TrimSpace(response.Revisions.Revision[0].Version))
		for _, candidate := range response.Revisions.Revision[1:] {
			if strings.ToLower(strings.TrimSpace(candidate.Version)) != revision {
				revision = "multiple"
				break
			}
		}
		build.Revision = revision
	}
	if response.WebURL != "" {
		webURL, err := t.validateWebURL(response.WebURL)
		if err != nil {
			return Build{}, fmt.Errorf("%w: teamcity build %s: %v", ErrDeployRejected, build.ID, err)
		}
		build.WebURL = webURL
	}
	return build, nil
}

// validateWebURL keeps the result link on the configured TeamCity host so a
// compromised or misconfigured response cannot turn the stage result into an
// arbitrary link.
func (t *TeamCity) validateWebURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || !strings.EqualFold(parsed.Host, t.base.Host) || parsed.Fragment != "" {
		return "", errors.New("build web URL is not an https URL on the configured TeamCity host")
	}
	return parsed.String(), nil
}

func (t *TeamCity) requestJSON(ctx context.Context, method, path string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode teamcity request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *t.base
	endpoint.Path = t.base.Path + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create teamcity request: %w", t.redact(err))
	}
	request.Header.Set("Authorization", "Bearer "+t.token)
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := t.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("teamcity %s %s request: %w", method, path, ctx.Err())
		}
		if errors.Is(err, errForeignRedirect) {
			return fmt.Errorf("teamcity %s %s: %w", method, path, errForeignRedirect)
		}
		return &TransientError{Err: fmt.Errorf("teamcity %s %s request failed: %w", method, path, t.redact(safeNetworkError(err)))}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, teamCityResponseLimit))
		statusErr := fmt.Errorf("teamcity %w", &HTTPStatusError{Method: method, Path: path, StatusCode: response.StatusCode})
		if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
			return &TransientError{Err: statusErr}
		}
		return statusErr
	}
	if responseBody == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, teamCityResponseLimit))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, teamCityResponseLimit+1))
	if err != nil {
		return fmt.Errorf("read teamcity response: %w", t.redact(safeNetworkError(err)))
	}
	if len(data) > teamCityResponseLimit {
		return errors.New("teamcity response exceeded size limit")
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return errors.New("decode teamcity response: invalid JSON")
	}
	return nil
}

// redact is a last line of defense: no code path formats the token, but a
// transport error must never be able to carry it into evidence or events.
func (t *TeamCity) redact(err error) error {
	if err == nil || t.token == "" || !strings.Contains(err.Error(), t.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.token, "[REDACTED]"))
}

// safeNetworkError drops the request URL, which can contain deployment paths,
// while preserving the transport failure and context errors.
func safeNetworkError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return fmt.Errorf("%s: %w", urlError.Op, urlError.Err)
	}
	return err
}

func limitText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
