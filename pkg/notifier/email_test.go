package notifier

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"
)

type smtpTranscript struct {
	auth, from string
	to         []string
	data       string
}

// fakeSMTP is an in-process loopback SMTP server speaking just enough of the
// protocol for net/smtp: EHLO, AUTH PLAIN, MAIL, RCPT, DATA and QUIT.
type fakeSMTP struct {
	listener    net.Listener
	transcripts chan smtpTranscript
	rejectRcpt  bool
	silent      bool
}

func startFakeSMTP(t *testing.T, configure func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSMTP{listener: listener, transcripts: make(chan smtpTranscript, 4)}
	if configure != nil {
		configure(server)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	return server
}

func (s *fakeSMTP) addr() string { return s.listener.Addr().String() }

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if s.silent {
		buffer := make([]byte, 1)
		_, _ = conn.Read(buffer) // hold the connection until the client gives up
		return
	}
	reader := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	var transcript smtpTranscript
	reply("220 fake.smtp ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			reply("250-fake.smtp")
			reply("250 AUTH PLAIN")
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			decoded, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN "):]))
			transcript.auth = string(decoded)
			reply("235 2.7.0 accepted")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			transcript.from = line[len("MAIL FROM:"):]
			reply("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.rejectRcpt {
				reply("550 5.1.1 <" + line[len("RCPT TO:"):] + "> mailbox unavailable")
				continue
			}
			transcript.to = append(transcript.to, line[len("RCPT TO:"):])
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				data.WriteString(dataLine)
			}
			transcript.data = data.String()
			reply("250 queued")
			s.transcripts <- transcript
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestEmailSendsPlainTextMessageThroughSMTP(t *testing.T) {
	server := startFakeSMTP(t, nil)
	channel := NewEmailChannel("leads", "smtp://bot:s3cr3t-pass@"+server.addr(), "ai-team <bot@example.test>", []string{"Lead <lead@example.test>", "qa@example.test"})
	if channel.Name() != "leads" {
		t.Fatalf("name=%q", channel.Name())
	}
	n := sampleNotification()
	n.Stage = "qa\r\nBcc: attacker@example.test"
	if err := channel.WithTimeout(5*time.Second).Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	got := <-server.transcripts
	if got.auth != "\x00bot\x00s3cr3t-pass" {
		t.Fatalf("auth=%q", got.auth)
	}
	if got.from != "<bot@example.test>" || strings.Join(got.to, ",") != "<lead@example.test>,<qa@example.test>" {
		t.Fatalf("envelope from=%q to=%v", got.from, got.to)
	}
	for _, want := range []string{
		"From: \"ai-team\" <bot@example.test>\r\n",
		"To: \"Lead\" <lead@example.test>, <qa@example.test>\r\n",
		"Message-ID: <ai-team-event-42@ai-team.invalid>\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\nhttps://ai-team.example/tasks/run-42\r\n",
	} {
		if !strings.Contains(got.data, want) {
			t.Fatalf("message lacks %q:\n%s", want, got.data)
		}
	}
	if strings.Contains(got.data, "\r\nBcc:") {
		t.Fatalf("header injection reached the message:\n%s", got.data)
	}
}

func TestEmailFailuresAreSanitized(t *testing.T) {
	rejecting := startFakeSMTP(t, func(s *fakeSMTP) { s.rejectRcpt = true })
	err := NewEmailChannel("leads", "smtp://bot:s3cr3t-pass@"+rejecting.addr(), "bot@example.test", []string{"lead@example.test"}).Send(context.Background(), sampleNotification())
	if err == nil || err.Error() != "email: SMTP recipient rejected with code 550" {
		t.Fatalf("error=%v", err)
	}

	silent := startFakeSMTP(t, func(s *fakeSMTP) { s.silent = true })
	err = NewEmailChannel("leads", "smtp://bot:s3cr3t-pass@"+silent.addr(), "bot@example.test", []string{"lead@example.test"}).
		WithTimeout(50*time.Millisecond).Send(context.Background(), sampleNotification())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("silent server error=%v", err)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	_ = closed.Close()
	err = NewEmailChannel("leads", "smtp://bot:s3cr3t-pass@"+address, "bot@example.test", []string{"lead@example.test"}).Send(context.Background(), sampleNotification())
	if err == nil || !strings.HasPrefix(err.Error(), "email: SMTP connect failed") {
		t.Fatalf("unreachable error=%v", err)
	}

	for _, raw := range []string{"", "http://smtp.example", "smtp://bot:s3cr3t-pass@", "smtp://smtp.example/path"} {
		err := NewEmailChannel("leads", raw, "bot@example.test", []string{"lead@example.test"}).Send(context.Background(), sampleNotification())
		if err == nil || strings.Contains(err.Error(), "s3cr3t") {
			t.Fatalf("invalid SMTP URL %q: error=%v", raw, err)
		}
	}
	if err := NewEmailChannel("leads", "smtp://"+rejecting.addr(), "not an address", []string{"lead@example.test"}).Send(context.Background(), sampleNotification()); err == nil || err.Error() != "email: sender address is invalid" {
		t.Fatalf("invalid sender error=%v", err)
	}
	if err := NewEmailChannel("leads", "smtp://"+rejecting.addr(), "bot@example.test", nil).Send(context.Background(), sampleNotification()); err == nil || err.Error() != "email: no recipients configured" {
		t.Fatalf("no recipients error=%v", err)
	}
}

func TestEmailRefusesCredentialsWithoutTLSToRemoteHost(t *testing.T) {
	server := startFakeSMTP(t, nil)
	_, port, _ := net.SplitHostPort(server.addr())
	channel := NewEmailChannel("leads", "smtp://bot:s3cr3t-pass@smtp.example.test:"+port, "bot@example.test", []string{"lead@example.test"})
	channel.dialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.addr())
	}
	err := channel.Send(context.Background(), sampleNotification())
	if err == nil || err.Error() != "email: SMTP starttls refused: server does not offer TLS" {
		t.Fatalf("error=%v", err)
	}
	select {
	case transcript := <-server.transcripts:
		t.Fatalf("message was sent without TLS: %+v", transcript)
	default:
	}
}

func TestEmailRequiresSTARTTLSForRemoteHostWithoutCredentials(t *testing.T) {
	server := startFakeSMTP(t, nil)
	_, port, _ := net.SplitHostPort(server.addr())
	channel := NewEmailChannel("leads", "smtp://smtp.example.test:"+port, "bot@example.test", []string{"lead@example.test"})
	channel.dialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.addr())
	}
	err := channel.Send(context.Background(), sampleNotification())
	if err == nil || err.Error() != "email: SMTP starttls refused: server does not offer TLS" {
		t.Fatalf("error=%v", err)
	}
	select {
	case transcript := <-server.transcripts:
		t.Fatalf("message was sent without TLS: %+v", transcript)
	default:
	}
}

func TestEmailAllowsPlainSMTPOnlyToLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.10.0.1": true, "::1": true,
		"smtp.example": false, "10.0.0.1": false, "localhost.example": false, "0.0.0.0": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q)=%v, want %v", host, got, want)
		}
	}
}
