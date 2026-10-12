package notifier

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
	"strconv"
	"strings"
	"time"
)

const maxResponseDrain = 64 << 10

// errRedirectRejected is returned by the redirect policy; its text never
// includes the redirect target.
var errRedirectRejected = errors.New("redirect to a different host was rejected")

// WebhookChannel posts a JSON document for each notification to an HTTPS
// endpoint read from the server environment.
type WebhookChannel struct {
	name    string
	rawURL  string
	client  *http.Client
	timeout time.Duration
}

// NewWebhookChannel creates a generic JSON webhook channel. client may be nil;
// the channel always applies its own redirect policy and timeout.
func NewWebhookChannel(name, rawURL string, client *http.Client) *WebhookChannel {
	return &WebhookChannel{name: name, rawURL: rawURL, client: client, timeout: DefaultDeliveryTimeout}
}

// WithTimeout overrides the per-delivery timeout.
func (c *WebhookChannel) WithTimeout(timeout time.Duration) *WebhookChannel {
	if timeout > 0 {
		c.timeout = timeout
	}
	return c
}

func (c *WebhookChannel) Name() string { return c.name }

type webhookPayload struct {
	EventID   int64          `json:"event_id"`
	Type      string         `json:"type"`
	RunID     string         `json:"run_id"`
	TaskID    int64          `json:"task_id,omitempty"`
	Stage     string         `json:"stage,omitempty"`
	Function  string         `json:"function,omitempty"`
	Severity  string         `json:"severity,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	TaskURL   string         `json:"task_url,omitempty"`
	Summary   string         `json:"summary"`
	Data      map[string]any `json:"data,omitempty"`
}

func (c *WebhookChannel) Send(ctx context.Context, n Notification) error {
	body, err := json.Marshal(webhookPayload{
		EventID: n.EventID, Type: n.Type, RunID: n.RunID, TaskID: n.TaskID, Stage: n.Stage,
		Function: n.Function, Severity: n.Severity, Timestamp: n.Timestamp.UTC(), TaskURL: n.TaskURL,
		Summary: n.Summary(), Data: n.Data,
	})
	if err != nil {
		return fmt.Errorf("webhook payload: %w", err)
	}
	return postJSON(ctx, c.client, c.timeout, "webhook", c.rawURL, body, n.EventID)
}

// SlackChannel posts the notification text to a Slack incoming webhook.
type SlackChannel struct {
	name    string
	rawURL  string
	client  *http.Client
	timeout time.Duration
}

// NewSlackChannel creates a Slack incoming-webhook channel. The URL itself is
// a credential and must come from the environment.
func NewSlackChannel(name, rawURL string, client *http.Client) *SlackChannel {
	return &SlackChannel{name: name, rawURL: rawURL, client: client, timeout: DefaultDeliveryTimeout}
}

// WithTimeout overrides the per-delivery timeout.
func (c *SlackChannel) WithTimeout(timeout time.Duration) *SlackChannel {
	if timeout > 0 {
		c.timeout = timeout
	}
	return c
}

func (c *SlackChannel) Name() string { return c.name }

func (c *SlackChannel) Send(ctx context.Context, n Notification) error {
	body, err := json.Marshal(map[string]string{"text": n.Text()})
	if err != nil {
		return fmt.Errorf("slack payload: %w", err)
	}
	return postJSON(ctx, c.client, c.timeout, "slack", c.rawURL, body, n.EventID)
}

// ValidateHTTPSURL checks a destination URL without echoing it in errors.
func ValidateHTTPSURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, errors.New("destination URL is not configured")
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("destination URL must be an absolute https URL without credentials")
	}
	return parsed, nil
}

func postJSON(ctx context.Context, base *http.Client, timeout time.Duration, kind, rawURL string, body []byte, eventID int64) error {
	destination, err := ValidateHTTPSURL(rawURL)
	if err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	client := sameHostClient(base, destination)
	if timeout <= 0 {
		timeout = DefaultDeliveryTimeout
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, destination.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: request could not be built", kind)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "ai-team-notifier")
	idempotency := "ai-team-event-" + strconv.FormatInt(eventID, 10)
	request.Header.Set("Idempotency-Key", idempotency)
	request.Header.Set("X-AI-Team-Event-ID", strconv.FormatInt(eventID, 10))
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %s", kind, describeTransportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseDrain))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("%s: HTTP %d", kind, response.StatusCode)
	}
	return nil
}

// sameHostClient copies base and forbids redirects that leave the original
// scheme and host. A redirect could otherwise forward the payload, or a
// credential embedded in the path, to an unrelated server.
func sameHostClient(base *http.Client, destination *url.URL) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errRedirectRejected
		}
		if request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, destination.Host) {
			return errRedirectRejected
		}
		return nil
	}
	return &client
}

// describeTransportError classifies a transport failure without including
// the request URL, which *url.Error would otherwise print.
func describeTransportError(err error) string {
	switch {
	case errors.Is(err, errRedirectRejected):
		return errRedirectRejected.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS verification failed"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op != "" {
		return "connection failed (" + opErr.Op + ")"
	}
	return "request failed"
}
