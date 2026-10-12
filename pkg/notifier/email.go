package notifier

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EmailChannel sends a plain-text e-mail through SMTP. The SMTP endpoint and
// credentials come from one environment value:
// smtp://user:password@host:587 (STARTTLS when offered) or
// smtps://user:password@host:465 (implicit TLS).
type EmailChannel struct {
	name    string
	smtpURL string
	from    string
	to      []string
	timeout time.Duration
	dialer  func(ctx context.Context, network, address string) (net.Conn, error)
	tls     *tls.Config
}

// NewEmailChannel creates an SMTP e-mail channel.
func NewEmailChannel(name, smtpURL, from string, to []string) *EmailChannel {
	dialer := &net.Dialer{}
	return &EmailChannel{
		name: name, smtpURL: smtpURL, from: from, to: append([]string(nil), to...),
		timeout: DefaultDeliveryTimeout, dialer: dialer.DialContext,
	}
}

// WithTimeout overrides the per-delivery timeout.
func (c *EmailChannel) WithTimeout(timeout time.Duration) *EmailChannel {
	if timeout > 0 {
		c.timeout = timeout
	}
	return c
}

// WithTLSConfig sets the TLS client configuration (root CAs) used for
// STARTTLS and smtps. ServerName is always taken from the SMTP host.
func (c *EmailChannel) WithTLSConfig(config *tls.Config) *EmailChannel {
	c.tls = config
	return c
}

func (c *EmailChannel) Name() string { return c.name }

type smtpEndpoint struct {
	host, address, username, password string
	implicitTLS                       bool
}

func parseSMTPURL(raw string) (smtpEndpoint, error) {
	if strings.TrimSpace(raw) == "" {
		return smtpEndpoint{}, errors.New("SMTP server is not configured")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "smtp" && parsed.Scheme != "smtps") || parsed.Hostname() == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return smtpEndpoint{}, errors.New("SMTP server must be an smtp:// or smtps:// URL with a host")
	}
	endpoint := smtpEndpoint{host: parsed.Hostname(), implicitTLS: parsed.Scheme == "smtps"}
	port := parsed.Port()
	if port == "" {
		port = "587"
		if endpoint.implicitTLS {
			port = "465"
		}
	}
	endpoint.address = net.JoinHostPort(endpoint.host, port)
	if parsed.User != nil {
		endpoint.username = parsed.User.Username()
		endpoint.password, _ = parsed.User.Password()
	}
	return endpoint, nil
}

func (c *EmailChannel) Send(ctx context.Context, n Notification) error {
	endpoint, err := parseSMTPURL(c.smtpURL)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	message, envelopeFrom, envelopeTo, err := c.message(n)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultDeliveryTimeout
	}
	sendContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.deliver(sendContext, endpoint, envelopeFrom, envelopeTo, message); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return nil
}

func (c *EmailChannel) tlsConfig(host string) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.tls != nil {
		config = c.tls.Clone()
	}
	config.ServerName = host
	return config
}

func (c *EmailChannel) deliver(ctx context.Context, endpoint smtpEndpoint, from string, to []string, message []byte) error {
	conn, err := c.dialer(ctx, "tcp", endpoint.address)
	if err != nil {
		return errors.New(smtpStepError("connect", err))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if endpoint.implicitTLS {
		conn = tls.Client(conn, c.tlsConfig(endpoint.host))
	}
	// The deadline may surface either as an i/o timeout or, after AfterFunc
	// closed the connection, as a closed-connection error.
	step := func(name string, err error) error {
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.New("SMTP " + name + " timed out")
		}
		return errors.New(smtpStepError(name, err))
	}
	client, err := smtp.NewClient(conn, endpoint.host)
	if err != nil {
		_ = conn.Close()
		return step("greeting", err)
	}
	defer func() { _ = client.Close() }()
	if err := step("hello", client.Hello("ai-team")); err != nil {
		return err
	}
	if !endpoint.implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := step("starttls", client.StartTLS(c.tlsConfig(endpoint.host))); err != nil {
				return err
			}
		}
	}
	if endpoint.username != "" {
		// smtp.PlainAuth refuses to send credentials without TLS except to
		// a loopback server, so a missing STARTTLS cannot leak the password.
		if err := step("auth", client.Auth(smtp.PlainAuth("", endpoint.username, endpoint.password, endpoint.host))); err != nil {
			return err
		}
	}
	if err := step("sender", client.Mail(from)); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := step("recipient", client.Rcpt(recipient)); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err := step("data", err); err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		return step("data", err)
	}
	if err := step("data", writer.Close()); err != nil {
		return err
	}
	return step("quit", client.Quit())
}

// smtpStepError describes an SMTP failure by step and reply code only; server
// replies and dial errors may echo addresses or credentials.
func smtpStepError(step string, err error) string {
	var protocol *textproto.Error
	if errors.As(err, &protocol) {
		return "SMTP " + step + " rejected with code " + strconv.Itoa(protocol.Code)
	}
	if strings.Contains(err.Error(), "unencrypted connection") {
		return "SMTP " + step + " refused: server does not offer TLS"
	}
	return "SMTP " + step + " failed: " + describeTransportError(err)
}

func (c *EmailChannel) message(n Notification) ([]byte, string, []string, error) {
	from, err := mail.ParseAddress(c.from)
	if err != nil {
		return nil, "", nil, errors.New("sender address is invalid")
	}
	recipients := make([]string, 0, len(c.to))
	envelope := make([]string, 0, len(c.to))
	for _, recipient := range c.to {
		address, err := mail.ParseAddress(recipient)
		if err != nil {
			return nil, "", nil, errors.New("recipient address is invalid")
		}
		recipients = append(recipients, address.String())
		envelope = append(envelope, address.Address)
	}
	if len(recipients) == 0 {
		return nil, "", nil, errors.New("no recipients configured")
	}
	timestamp := n.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	var buffer bytes.Buffer
	header := func(name, value string) {
		buffer.WriteString(name + ": " + singleLine(value) + "\r\n")
	}
	header("From", from.String())
	header("To", strings.Join(recipients, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", n.Summary()))
	header("Date", timestamp.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<ai-team-event-"+strconv.FormatInt(n.EventID, 10)+"@ai-team.invalid>")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	buffer.WriteString("\r\n")
	buffer.WriteString(strings.ReplaceAll(n.Text(), "\n", "\r\n"))
	buffer.WriteString("\r\n")
	return buffer.Bytes(), from.Address, envelope, nil
}
