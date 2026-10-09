package worker

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenAIEgressBridgeTunnelsOnlyExactOpenAIConnect(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fake-openai" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "fake-openai-upstream")
	})
	fake := newTLSOpenAIUpstream(t, upstream)
	defer fake.Close()

	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "test-capability", func(context.Context) (net.Conn, error) {
		return net.Dial("tcp", strings.TrimPrefix(fake.URL, "https://"))
	})
	mustNoError(t, err)
	defer server.close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyURL, closeBridge, err := StartOpenAIEgressBridge(ctx, socket, "test-capability")
	mustNoError(t, err)
	defer closeBridge()
	t.Setenv("HTTPS_PROXY", proxyURL)
	t.Setenv("HTTP_PROXY", proxyURL)
	t.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")

	parsedProxy, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	mustNoError(t, err)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(parsedProxy),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // fake upstream only
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("https://api.openai.com/fake-openai")
	mustNoError(t, err)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	mustNoError(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if string(body) != "fake-openai-upstream" {
		t.Fatalf("body = %q", body)
	}

	for _, address := range []string{"https://example.com/", "https://api.openai.com:444/"} {
		request, requestErr := http.NewRequest(http.MethodGet, address, nil)
		mustNoError(t, requestErr)
		_, requestErr = client.Do(request)
		if requestErr == nil {
			t.Fatalf("proxy must reject %s", address)
		}
	}
}

func TestOpenAIEgressControllerRequiresCapabilityAndExactTarget(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "known-capability", func(context.Context) (net.Conn, error) {
		return nil, fmt.Errorf("dial should not happen for rejected CONNECT")
	})
	mustNoError(t, err)
	defer server.close()

	for _, test := range []struct {
		name   string
		target string
		token  string
	}{
		{name: "wrong host", target: "example.com:443", token: "known-capability"},
		{name: "wrong port", target: "api.openai.com:444", token: "known-capability"},
		{name: "trailing dot", target: "api.openai.com.:443", token: "known-capability"},
		{name: "missing capability", target: "api.openai.com:443"},
		{name: "wrong capability", target: "api.openai.com:443", token: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, dialErr := net.Dial("unix", socket)
			mustNoError(t, dialErr)
			defer func() { _ = conn.Close() }()
			_, writeErr := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", test.target, test.target)
			mustNoError(t, writeErr)
			if test.token != "" {
				_, writeErr = fmt.Fprintf(conn, "Proxy-Authorization: Bearer %s\r\n", test.token)
				mustNoError(t, writeErr)
			}
			_, writeErr = io.WriteString(conn, "\r\n")
			mustNoError(t, writeErr)
			response, readErr := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			mustNoError(t, readErr)
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
			}
		})
	}
}

func TestPublicUnicastIPRejectsSpecialAndReservedRanges(t *testing.T) {
	if publicUnicastIP(nil) {
		t.Fatal("nil DNS result was classified as a public unicast address")
	}
	for _, test := range []struct {
		address string
		want    bool
	}{
		{address: "8.8.8.8", want: true},
		{address: "1.1.1.1", want: true},
		{address: "2606:4700:4700::1111", want: true},
		{address: "2001:4860:4860::8888", want: true},
		{address: "4000::1"},         // outside IANA's global-unicast allocation
		{address: "5fff::1"},         // outside IANA's global-unicast allocation
		{address: "100.100.100.200"}, // carrier-grade/shared range
		{address: "100.64.0.1"},
		{address: "198.18.0.1"},   // benchmarking
		{address: "198.51.100.4"}, // documentation
		{address: "203.0.113.10"}, // documentation
		{address: "192.0.2.1"},
		{address: "240.0.0.1"},         // reserved
		{address: "2001:db8::1"},       // documentation
		{address: "2001:20::1"},        // special-purpose allocation
		{address: "2002:0808:0808::1"}, // 6to4 transition
		{address: "64:ff9b:1::1"},      // local-use translation
	} {
		t.Run(test.address, func(t *testing.T) {
			ip := net.ParseIP(test.address)
			if got := publicUnicastIP(ip); got != test.want {
				t.Fatalf("publicUnicastIP(%s) = %v, want %v", test.address, got, test.want)
			}
		})
	}
}

func TestOpenAIEgressControllerShutdownClosesHijackedTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoError(t, err)
	defer func() { _ = listener.Close() }()
	upstreamClosed := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			close(upstreamClosed)
			return
		}
		defer func() { _ = connection.Close() }()
		var one [1]byte
		_, _ = connection.Read(one[:]) // deliberately stay open until controller closes it
		close(upstreamClosed)
	}()
	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "known-capability", func(context.Context) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	})
	mustNoError(t, err)
	worker, err := net.Dial("unix", socket)
	mustNoError(t, err)
	if _, err := io.WriteString(worker, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\nProxy-Authorization: Bearer known-capability\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(worker), &http.Request{Method: http.MethodConnect})
	mustNoError(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	// A worker-side disconnect must close the stalled upstream even though it
	// never sends its own FIN.
	_ = worker.Close()
	select {
	case <-upstreamClosed:
	case <-time.After(time.Second):
		t.Fatal("worker disconnect left stalled OpenAI upstream connection open")
	}
	server.close()
}

func TestOpenAIEgressControllerShutdownClosesActiveTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoError(t, err)
	defer func() { _ = listener.Close() }()
	upstreamClosed := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			close(upstreamClosed)
			return
		}
		defer func() { _ = connection.Close() }()
		var one [1]byte
		_, _ = connection.Read(one[:])
		close(upstreamClosed)
	}()
	socket := shortOpenAIEgressSocket(t)
	server, err := startOpenAIEgressServerWith(socket, "known-capability", func(context.Context) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	})
	mustNoError(t, err)
	defer server.close()
	worker, err := net.Dial("unix", socket)
	mustNoError(t, err)
	defer func() { _ = worker.Close() }()
	if _, err := io.WriteString(worker, "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\nProxy-Authorization: Bearer known-capability\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(worker), &http.Request{Method: http.MethodConnect})
	mustNoError(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	server.close()
	select {
	case <-upstreamClosed:
	case <-time.After(time.Second):
		t.Fatal("controller shutdown left stalled OpenAI upstream connection open")
	}
}

func TestOpenAIEgressBridgeClosesConnectionsOnCancellation(t *testing.T) {
	socket := shortOpenAIEgressSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	proxyURL, closeBridge, err := StartOpenAIEgressBridge(ctx, socket, "test-capability")
	mustNoError(t, err)
	defer closeBridge()
	proxy, err := url.Parse(proxyURL)
	mustNoError(t, err)
	connection, err := net.DialTimeout("tcp", proxy.Host, time.Second)
	mustNoError(t, err)
	defer func() { _ = connection.Close() }()
	_, err = io.WriteString(connection, "CONNECT api.openai.com:443 HTTP/1.1\r\n")
	mustNoError(t, err)
	cancel()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	var response [1]byte
	if _, err := connection.Read(response[:]); err == nil {
		t.Fatal("bridge left an accepted connection open after cancellation")
	}
}

func newTLSOpenAIUpstream(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	return server
}

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func shortOpenAIEgressSocket(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix egress transport is not supported on Windows")
	}
	dir, err := os.MkdirTemp("/tmp", "oai-")
	mustNoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "e.sock")
}
