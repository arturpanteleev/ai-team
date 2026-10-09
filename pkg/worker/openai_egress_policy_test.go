package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeOpenAIResolver struct {
	ips []net.IPAddr
	err error
}

func (r fakeOpenAIResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if host != openAIEgressHost {
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	return r.ips, r.err
}

type fakeOpenAIDialer struct {
	addresses []string
	conn      net.Conn
	err       error
}

func (d *fakeOpenAIDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("unexpected network %q", network)
	}
	d.addresses = append(d.addresses, address)
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func TestOpenAIEgressServerConstructionAndLifecycleBranches(t *testing.T) {
	if _, err := startOpenAIEgressServerWith("", "token", nil); err == nil {
		t.Fatal("empty socket path should fail")
	}
	if _, err := startOpenAIEgressServerWith("/tmp/unused.sock", "", nil); err == nil {
		t.Fatal("empty token should fail")
	}
	if _, err := startOpenAIEgressServerWithToken("unused.sock", func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }, nil); err == nil {
		t.Fatal("random capability error should propagate")
	}
	occupied := shortOpenAIEgressSocket(t)
	listener, err := net.Listen("unix", occupied)
	mustNoError(t, err)
	if _, err := startOpenAIEgressServerWith(occupied, "token", nil); err == nil {
		t.Fatal("existing socket should fail")
	}
	_ = listener.Close()

	server, err := startOpenAIEgressServerWithDial(shortOpenAIEgressSocket(t), func(context.Context) (net.Conn, error) {
		return nil, errors.New("not used")
	})
	mustNoError(t, err)
	server.close()
	server.close()
	server, err = startOpenAIEgressServer(shortOpenAIEgressSocket(t))
	mustNoError(t, err)
	server.close()
	var nilServer *openAIEgressServer
	nilServer.close()
}

func TestOpenAIEgressServerReturnsBadGatewayWhenDialFails(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "known-capability", func(context.Context) (net.Conn, error) {
		return nil, errors.New("upstream unavailable")
	})
	mustNoError(t, err)
	defer server.close()
	conn, err := net.Dial("unix", socket)
	mustNoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\nProxy-Authorization: Bearer known-capability\r\n\r\n")
	mustNoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	mustNoError(t, err)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
}

func TestOpenAIEgressServerTrackingBranches(t *testing.T) {
	server := &openAIEgressServer{active: make(map[net.Conn]struct{})}
	left, leftPeer := net.Pipe()
	right, rightPeer := net.Pipe()
	defer func() { _ = leftPeer.Close() }()
	defer func() { _ = rightPeer.Close() }()
	if !server.track(left, right) {
		t.Fatal("open server rejected connections")
	}
	if len(server.active) != 2 {
		t.Fatalf("tracked %d connections, want 2", len(server.active))
	}
	server.untrack(left, right)
	if len(server.active) != 0 {
		t.Fatalf("untracked connections remain: %d", len(server.active))
	}
	server.closed = true
	if server.track(left, right) {
		t.Fatal("closed server accepted connections")
	}
	_ = left.Close()
	_ = right.Close()
}

func TestDialOpenAIHostPolicyAndFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialOpenAIHost(canceled); err == nil {
		t.Fatal("canceled DNS lookup should fail")
	}
	lookupErr := errors.New("resolver unavailable")
	if _, err := dialOpenAIHostWith(context.Background(), fakeOpenAIResolver{err: lookupErr}, &fakeOpenAIDialer{}); err == nil || !strings.Contains(err.Error(), "resolve OpenAI endpoint") {
		t.Fatalf("resolver error = %v", err)
	}
	for _, ips := range [][]net.IPAddr{
		nil,
		{{IP: net.ParseIP("127.0.0.1")}},
		{{IP: net.ParseIP("192.0.2.10")}},
	} {
		dialer := &fakeOpenAIDialer{}
		if _, err := dialOpenAIHostWith(context.Background(), fakeOpenAIResolver{ips: ips}, dialer); err == nil || !strings.Contains(err.Error(), "no public addresses") {
			t.Fatalf("addresses %v error = %v, want no public addresses", ips, err)
		}
		if len(dialer.addresses) != 0 {
			t.Fatalf("attempted a non-public address: %v", dialer.addresses)
		}
	}
	connectErr := errors.New("connect refused")
	dialer := &fakeOpenAIDialer{err: connectErr}
	_, err := dialOpenAIHostWith(context.Background(), fakeOpenAIResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, dialer)
	if err == nil || !strings.Contains(err.Error(), "connect to OpenAI endpoint") {
		t.Fatalf("dial error = %v", err)
	}
	client, upstream := net.Pipe()
	defer func() { _ = upstream.Close() }()
	dialer = &fakeOpenAIDialer{conn: client}
	conn, err := dialOpenAIHostWith(context.Background(), fakeOpenAIResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("8.8.8.8")}}}, dialer)
	mustNoError(t, err)
	if conn != client || len(dialer.addresses) != 1 || dialer.addresses[0] != "8.8.8.8:443" {
		t.Fatalf("dial result %v, attempted %v", conn, dialer.addresses)
	}
	_ = conn.Close()
}

func TestOpenAIEgressBridgeSetupErrors(t *testing.T) {
	noListen := func() (net.Listener, error) { return nil, errors.New("listen failure") }
	if _, _, err := startOpenAIEgressBridge(context.Background(), "", "token", nil, noListen); err == nil {
		t.Fatal("empty socket should fail")
	}
	if _, _, err := startOpenAIEgressBridge(context.Background(), "socket", "", nil, noListen); err == nil {
		t.Fatal("empty token should fail")
	}
	if _, _, err := startOpenAIEgressBridge(context.Background(), "socket", "token", func() error { return errors.New("loopback failure") }, noListen); err == nil {
		t.Fatal("loopback setup failure should propagate")
	}
	if _, _, err := startOpenAIEgressBridge(context.Background(), "socket", "token", func() error { return nil }, noListen); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("listener error = %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	mustNoError(t, err)
	proxy, closeFn, err := startOpenAIEgressBridge(context.Background(), "socket", "token", func() error { return nil }, func() (net.Listener, error) { return listener, nil })
	mustNoError(t, err)
	if !strings.HasPrefix(proxy, "http://127.0.0.1:") {
		t.Fatalf("proxy URL = %q", proxy)
	}
	closeFn()
	closeFn()
}

func TestOpenAIEgressBridgeRejectsInvalidRequestsAndUnavailableSocket(t *testing.T) {
	for _, test := range []struct {
		name       string
		request    string
		socketPath string
		wantStatus int
	}{
		{
			name: "non-CONNECT request", request: "GET / HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing controller socket",
			request:    "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n",
			socketPath: filepath.Join(t.TempDir(), "missing.sock"), wantStatus: http.StatusBadGateway,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			go bridgeOpenAIClient(client, test.socketPath, "scoped-token", make(chan struct{}))
			if _, err := io.WriteString(peer, test.request); err != nil {
				t.Fatalf("write bridge request: %v", err)
			}
			response, err := http.ReadResponse(bufio.NewReader(peer), &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("read bridge response: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("bridge status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestOpenAIEgressBridgeClosesUpstreamWhenClientLeavesDuringHandshake(t *testing.T) {
	shortDir, err := os.MkdirTemp("/tmp", "ai-egr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
	socketPath := filepath.Join(shortDir, "proxy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	upstreamClosed := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			close(upstreamClosed)
			return
		}
		defer func() { _ = connection.Close() }()
		if _, err := http.ReadRequest(bufio.NewReader(connection)); err != nil {
			close(upstreamClosed)
			return
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_, _ = io.Copy(io.Discard, connection)
		close(upstreamClosed)
	}()

	client, peer := net.Pipe()
	go bridgeOpenAIClient(client, socketPath, "scoped-token", make(chan struct{}))
	if _, err := io.WriteString(peer, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n"); err != nil {
		_ = peer.Close()
		t.Fatalf("write bridge request: %v", err)
	}
	_ = peer.Close()
	select {
	case <-upstreamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge left the controller-side connection open after the client disconnected")
	}
}

type delayedOpenAIEgressListener struct {
	started chan struct{}
	release chan struct{}
	conn    net.Conn
}

func (l *delayedOpenAIEgressListener) Accept() (net.Conn, error) {
	close(l.started)
	<-l.release
	return l.conn, nil
}

func (l *delayedOpenAIEgressListener) Close() error { return nil }
func (l *delayedOpenAIEgressListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

func TestOpenAIEgressBridgeClosesConnectionAcceptedAfterShutdown(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()
	listener := &delayedOpenAIEgressListener{
		started: make(chan struct{}),
		release: make(chan struct{}),
		conn:    serverConn,
	}
	_, closeBridge, err := startOpenAIEgressBridge(context.Background(), "socket", "token", func() error {
		return nil
	}, func() (net.Listener, error) {
		return listener, nil
	})
	mustNoError(t, err)
	<-listener.started
	closeBridge()
	close(listener.release)

	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection accepted after shutdown remained open")
	} else if !errors.Is(err, io.EOF) {
		t.Fatalf("read from connection accepted after shutdown = %v, want EOF", err)
	}
}

func TestBridgeOpenAIClientRejectsMalformedAndUnavailableRequests(t *testing.T) {
	for _, request := range []string{
		"GET / HTTP/1.1\r\nHost: localhost\r\n\r\n",
		"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
		"this is not http\r\n\r\n",
	} {
		client, proxy := net.Pipe()
		done := make(chan struct{})
		go bridgeOpenAIClient(proxy, filepath.Join(t.TempDir(), "missing.sock"), "token", done)
		_, _ = io.WriteString(client, request)
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
		if err == nil {
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("request %q got status %d", request, response.StatusCode)
			}
		} else if !strings.Contains(request, "this is not") {
			t.Fatalf("request %q read response: %v", request, err)
		}
		_ = client.Close()
	}
	client, proxy := net.Pipe()
	go bridgeOpenAIClient(proxy, filepath.Join(t.TempDir(), "missing.sock"), "token", make(chan struct{}))
	_, _ = io.WriteString(client, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n")
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	mustNoError(t, err)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
	_ = client.Close()
}

func TestBridgeOpenAIClientForwardsControllerDenial(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	listener, err := net.Listen("unix", socket)
	mustNoError(t, err)
	defer func() { _ = listener.Close() }()
	go func() {
		upstream, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = upstream.Close() }()
		_, _ = http.ReadRequest(bufio.NewReader(upstream))
		_, _ = io.WriteString(upstream, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
	}()
	client, proxy := net.Pipe()
	defer func() { _ = client.Close() }()
	go bridgeOpenAIClient(proxy, socket, "secret-token", make(chan struct{}))
	_, err = io.WriteString(client, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\nProxy-Authorization: Bearer client-value\r\n\r\n")
	mustNoError(t, err)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	mustNoError(t, err)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
	_ = client.Close()
}

func TestBridgeOpenAIClientClosesOnMalformedControllerResponse(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	listener, err := net.Listen("unix", socket)
	mustNoError(t, err)
	defer func() { _ = listener.Close() }()
	responseWritten := make(chan error, 1)
	go func() {
		upstream, acceptErr := listener.Accept()
		if acceptErr != nil {
			responseWritten <- acceptErr
			return
		}
		defer func() { _ = upstream.Close() }()
		if _, readErr := http.ReadRequest(bufio.NewReader(upstream)); readErr != nil {
			responseWritten <- readErr
			return
		}
		_, writeErr := io.WriteString(upstream, "this is not an HTTP response\r\n\r\n")
		responseWritten <- writeErr
	}()
	client, proxy := net.Pipe()
	defer func() { _ = client.Close() }()
	go bridgeOpenAIClient(proxy, socket, "secret-token", make(chan struct{}))
	_, err = io.WriteString(client, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n")
	mustNoError(t, err)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, readErr := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if readErr == nil {
		t.Fatal("malformed controller response unexpectedly produced a client response")
	}
	var timeout net.Error
	if errors.As(readErr, &timeout) && timeout.Timeout() {
		t.Fatalf("timed out waiting for malformed controller response to be rejected: %v", readErr)
	}
	if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("expected bridge closure after malformed controller response, got %T: %v", readErr, readErr)
	}
	select {
	case writeErr := <-responseWritten:
		if writeErr != nil {
			t.Fatalf("controller failed to write malformed response: %v", writeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not finish writing malformed response")
	}
	_ = client.Close()
}

func TestBridgeOpenAIClientHandlesDisconnectedWorker(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	listener, err := net.Listen("unix", socket)
	mustNoError(t, err)
	defer func() { _ = listener.Close() }()
	requestRead := make(chan struct{})
	upstreamClosed := make(chan struct{})
	go func() {
		upstream, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = upstream.Close() }()
		_, _ = http.ReadRequest(bufio.NewReader(upstream))
		close(requestRead)
		if _, writeErr := io.WriteString(upstream, "HTTP/1.1 200 Connection Established\r\n\r\n"); writeErr != nil {
			close(upstreamClosed)
			return
		}
		_, _ = io.Copy(io.Discard, upstream)
		close(upstreamClosed)
	}()
	client, proxy := net.Pipe()
	defer func() { _ = client.Close() }()
	bridgeExited := make(chan struct{})
	go func() {
		bridgeOpenAIClient(proxy, socket, "secret-token", make(chan struct{}))
		close(bridgeExited)
	}()
	_, err = io.WriteString(client, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n")
	mustNoError(t, err)
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("controller did not receive CONNECT request")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read successful CONNECT response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	_ = client.Close()
	for name, ch := range map[string]<-chan struct{}{"upstream close": upstreamClosed, "bridge exit": bridgeExited} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s after worker disconnect", name)
		}
	}
}

func TestOpenAIEgressServerSocketPermissions(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "capability", func(context.Context) (net.Conn, error) { return nil, errors.New("unused") })
	mustNoError(t, err)
	info, err := os.Stat(socket)
	mustNoError(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
	}
	server.close()
}
