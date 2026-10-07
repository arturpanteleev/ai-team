package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	openAIEgressSocketEnv = "AI_TEAM_OPENAI_EGRESS_SOCKET"
	openAIEgressTokenEnv  = "AI_TEAM_OPENAI_EGRESS_TOKEN"
	openAIEgressProxyEnv  = "AI_TEAM_OPENAI_EGRESS_PROXY"
	openAIEgressHost      = "api.openai.com"
	openAIEgressPort      = "443"
)

const OpenAIEgressSocketEnv = openAIEgressSocketEnv
const OpenAIEgressTokenEnv = openAIEgressTokenEnv

type openAIEgressServer struct {
	listener net.Listener
	server   *http.Server
	socket   string
	token    string
	activeMu sync.Mutex
	active   map[net.Conn]struct{}
	closed   bool
}

func startOpenAIEgressServer(socketPath string) (*openAIEgressServer, error) {
	return startOpenAIEgressServerWithDial(socketPath, dialOpenAIHost)
}

func startOpenAIEgressServerWithDial(socketPath string, dial func(context.Context) (net.Conn, error)) (*openAIEgressServer, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("OpenAI egress capability: %w", err)
	}
	return startOpenAIEgressServerWith(socketPath, hex.EncodeToString(raw[:]), dial)
}

func startOpenAIEgressServerWith(socketPath, token string, dial func(context.Context) (net.Conn, error)) (*openAIEgressServer, error) {
	if token == "" || socketPath == "" {
		return nil, errors.New("OpenAI egress socket and capability are required")
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on OpenAI egress socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("secure OpenAI egress socket: %w", err)
	}
	result := &openAIEgressServer{listener: listener, socket: socketPath, token: token, active: make(map[net.Conn]struct{})}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || !sameCapability(r.Header.Get("Proxy-Authorization"), token) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !allowedOpenAIConnectTarget(r.Host) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		upstream, err := dial(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		client, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := rw.Flush(); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		if !result.track(client, upstream) {
			return
		}
		defer result.untrack(client, upstream)
		pipeConnectionsClosingBoth(client, upstream, nil, rw.Reader)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	result.server = server
	go func() { _ = server.Serve(listener) }()
	return result, nil
}

func (s *openAIEgressServer) close() {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	s.closed = true
	for connection := range s.active {
		_ = connection.Close()
	}
	s.activeMu.Unlock()
	_ = s.server.Close()
	_ = os.Remove(s.socket)
}

func (s *openAIEgressServer) track(connections ...net.Conn) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.closed {
		for _, connection := range connections {
			_ = connection.Close()
		}
		return false
	}
	for _, connection := range connections {
		s.active[connection] = struct{}{}
	}
	return true
}

func (s *openAIEgressServer) untrack(connections ...net.Conn) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	for _, connection := range connections {
		delete(s.active, connection)
	}
}

func sameCapability(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	return len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func allowedOpenAIConnectTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host != openAIEgressHost || port != openAIEgressPort {
		return false
	}
	return true
}

func dialOpenAIHost(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, openAIEgressHost)
	if err != nil {
		return nil, fmt.Errorf("resolve OpenAI endpoint")
	}
	var lastErr error
	dialer := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	for _, item := range ips {
		if !publicUnicastIP(item.IP) {
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(item.IP.String(), openAIEgressPort))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		return nil, errors.New("OpenAI DNS returned no public addresses")
	}
	return nil, fmt.Errorf("connect to OpenAI endpoint: %w", lastErr)
}

func publicUnicastIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if len(ip) == net.IPv6len {
		// IANA allocates global unicast IPv6 from 2000::/3. Go's
		// IsGlobalUnicast also accepts other unicast-looking IPv6 space, so
		// require this allocation before considering its special-use holes.
		_, globalUnicast, _ := net.ParseCIDR("2000::/3")
		if !globalUnicast.Contains(ip) {
			return false
		}
	}
	// IsGlobalUnicast reports address classification, not whether an address
	// is globally reachable. Exclude IANA special-purpose ranges (including
	// documentation, benchmarking, shared-address, and transition networks).
	// Prefixes track IANA's IPv4/IPv6 Special-Purpose Address Registries and
	// include documentation and transition blocks. They are intentionally
	// conservative: a DNS answer in any special-use allocation is rejected even
	// if a narrower exception is globally reachable.
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48",
		"100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16",
	} {
		_, prefix, _ := net.ParseCIDR(cidr)
		if strings.Contains(cidr, ":") != (len(ip) == net.IPv6len) {
			continue
		}
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// StartOpenAIEgressBridge exposes the controller's restricted Unix-socket
// CONNECT service to HTTP clients inside a network namespace. The bridge only
// accepts api.openai.com:443 CONNECT requests; it never accepts absolute-form
// requests or logs request headers/bodies.
func StartOpenAIEgressBridge(ctx context.Context, socketPath, token string) (string, func(), error) {
	if socketPath == "" || token == "" {
		return "", func() {}, errors.New("OpenAI egress socket and capability are required")
	}
	if err := ensureOpenAIEgressLoopback(); err != nil {
		return "", func() {}, fmt.Errorf("enable namespace-local loopback: %w", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", func() {}, fmt.Errorf("listen on namespace-local OpenAI proxy: %w", err)
	}
	var once sync.Once
	done := make(chan struct{})
	var connectionsMu sync.Mutex
	connections := make(map[net.Conn]struct{})
	closed := false
	closeFn := func() {
		once.Do(func() {
			connectionsMu.Lock()
			closed = true
			close(done)
			_ = listener.Close()
			for connection := range connections {
				_ = connection.Close()
			}
			connectionsMu.Unlock()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			closeFn()
		case <-done:
		}
	}()
	go func() {
		for {
			client, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connectionsMu.Lock()
			if closed {
				connectionsMu.Unlock()
				_ = client.Close()
				return
			}
			connections[client] = struct{}{}
			connectionsMu.Unlock()
			go func() {
				defer func() {
					connectionsMu.Lock()
					delete(connections, client)
					connectionsMu.Unlock()
				}()
				bridgeOpenAIClient(client, socketPath, token, done)
			}()
		}
	}()
	return "http://" + listener.Addr().String(), closeFn, nil
}

func bridgeOpenAIClient(client net.Conn, socketPath, token string, done <-chan struct{}) {
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(client)
	req, err := http.ReadRequest(reader)
	if err != nil || req.Method != http.MethodConnect || !allowedOpenAIConnectTarget(req.Host) {
		_, _ = io.WriteString(client, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	_ = client.SetDeadline(time.Time{})
	upstream, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer func() { _ = upstream.Close() }()
	stopCancellation := make(chan struct{})
	defer close(stopCancellation)
	go func() {
		select {
		case <-done:
			_ = client.Close()
			_ = upstream.Close()
		case <-stopCancellation:
		}
	}()
	if _, err := fmt.Fprintf(upstream, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\nProxy-Authorization: Bearer %s\r\n\r\n", openAIEgressHost, openAIEgressPort, openAIEgressHost, openAIEgressPort, token); err != nil {
		return
	}
	upstreamReader := bufio.NewReader(upstream)
	response, err := http.ReadResponse(upstreamReader, req)
	if err != nil {
		return
	}
	if response.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(client, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", response.StatusCode, http.StatusText(response.StatusCode))
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	pipeConnectionsClosingBoth(client, upstream, upstreamReader, reader)
}

// pipeConnectionsClosingBoth treats either endpoint closing as termination of
// the CONNECT tunnel. Unlike a generic TCP relay, a provider request has no
// useful half-open phase; closing both sockets also releases the other copier
// when an upstream stalls after the worker disconnects.
func pipeConnectionsClosingBoth(a, b net.Conn, pending ...io.Reader) {
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn, buffered io.Reader) {
		reader := io.Reader(src)
		if buffered != nil {
			reader = io.MultiReader(buffered, src)
		}
		_, _ = io.Copy(dst, reader)
		done <- struct{}{}
	}
	var leftPending, rightPending io.Reader
	if len(pending) > 0 {
		leftPending = pending[0]
	}
	if len(pending) > 1 {
		rightPending = pending[1]
	}
	go copyHalf(a, b, leftPending)
	go copyHalf(b, a, rightPending)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
