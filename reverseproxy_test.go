package touka

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReverseProxyForwardingAndHopHeaders(t *testing.T) {
	t.Helper()

	type backendRequestSnapshot struct {
		Path            string
		RawQuery        string
		Host            string
		Connection      string
		RemovedHeader   string
		Forwarded       string
		XForwardedFor   string
		XForwardedHost  string
		XForwardedProto string
		Via             []string
		TE              string
		UserAgent       string
	}

	var got backendRequestSnapshot
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = backendRequestSnapshot{
			Path:            r.URL.Path,
			RawQuery:        r.URL.RawQuery,
			Host:            r.Host,
			Connection:      r.Header.Get("Connection"),
			RemovedHeader:   r.Header.Get("X-Remove-Me"),
			Forwarded:       r.Header.Get("Forwarded"),
			XForwardedFor:   r.Header.Get("X-Forwarded-For"),
			XForwardedHost:  r.Header.Get("X-Forwarded-Host"),
			XForwardedProto: r.Header.Get("X-Forwarded-Proto"),
			Via:             append([]string(nil), r.Header.Values("Via")...),
			TE:              r.Header.Get("Te"),
			UserAgent:       r.Header.Get("User-Agent"),
		}

		w.Header().Set("Connection", "X-Backend-Secret")
		w.Header().Set("X-Backend-Secret", "remove-me")
		w.Header().Add("Via", "1.0 upstream")
		w.Header().Add("Trailer", "X-Upstream-Trailer")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "proxied")
		w.Header().Set("X-Upstream-Trailer", "done")
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL + "/base?from=target")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/api/*path", ReverseProxy(ReverseProxyConfig{
		Target:           target,
		ForwardedHeaders: ForwardedBoth,
		ForwardedBy:      "proxy-node",
		Via:              "proxy.test",
	}))

	req := httptest.NewRequest(http.MethodGet, "http://client.example/api/ping?q=2", nil)
	req.Host = "client.example"
	req.RemoteAddr = "198.51.100.10:4567"
	req.Header.Set("Connection", "X-Remove-Me")
	req.Header.Set("X-Remove-Me", "client-secret")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "edge.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=203.0.113.9")
	req.Header.Set("Te", "trailers")

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	resp := rr.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()

	if string(body) != "proxied" {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got.Path != "/base/api/ping" {
		t.Fatalf("unexpected upstream path: %q", got.Path)
	}
	if got.RawQuery != "from=target&q=2" {
		t.Fatalf("unexpected upstream raw query: %q", got.RawQuery)
	}
	if got.Host != strings.TrimPrefix(backend.URL, "http://") {
		t.Fatalf("unexpected upstream host: %q", got.Host)
	}
	if got.Connection != "" {
		t.Fatalf("connection header should be stripped, got %q", got.Connection)
	}
	if got.RemovedHeader != "" {
		t.Fatalf("connection-token header should be stripped, got %q", got.RemovedHeader)
	}
	if got.XForwardedFor != "203.0.113.9, 198.51.100.10" {
		t.Fatalf("unexpected X-Forwarded-For: %q", got.XForwardedFor)
	}
	if got.XForwardedHost != "edge.example" {
		t.Fatalf("unexpected X-Forwarded-Host: %q", got.XForwardedHost)
	}
	if got.XForwardedProto != "https" {
		t.Fatalf("unexpected X-Forwarded-Proto: %q", got.XForwardedProto)
	}
	if got.TE != "trailers" {
		t.Fatalf("unexpected TE header: %q", got.TE)
	}
	if got.UserAgent != "" {
		t.Fatalf("expected empty user-agent suppression, got %q", got.UserAgent)
	}
	if !strings.Contains(got.Forwarded, "for=203.0.113.9") {
		t.Fatalf("forwarded header missing prior hop: %q", got.Forwarded)
	}
	if !strings.Contains(got.Forwarded, "for=198.51.100.10") {
		t.Fatalf("forwarded header missing client ip: %q", got.Forwarded)
	}
	if !strings.Contains(got.Forwarded, "by=proxy-node") {
		t.Fatalf("forwarded header missing by token: %q", got.Forwarded)
	}
	if !strings.Contains(got.Forwarded, "host=client.example") {
		t.Fatalf("forwarded header missing host: %q", got.Forwarded)
	}
	if !strings.Contains(got.Forwarded, "proto=http") {
		t.Fatalf("forwarded header missing proto: %q", got.Forwarded)
	}
	if len(got.Via) != 1 || got.Via[0] != "1.1 proxy.test" {
		t.Fatalf("unexpected upstream Via headers: %#v", got.Via)
	}
	if resp.Header.Get("Connection") != "" {
		t.Fatalf("response connection header should be stripped, got %q", resp.Header.Get("Connection"))
	}
	if resp.Header.Get("X-Backend-Secret") != "" {
		t.Fatalf("response connection-token header should be stripped, got %q", resp.Header.Get("X-Backend-Secret"))
	}
	if gotVia := resp.Header.Values("Via"); len(gotVia) != 2 || gotVia[0] != "1.0 upstream" || gotVia[1] != "1.1 proxy.test" {
		t.Fatalf("unexpected response Via headers: %#v", gotVia)
	}
	if resp.Trailer.Get("X-Upstream-Trailer") != "done" {
		t.Fatalf("unexpected proxied trailer: %q", resp.Trailer.Get("X-Upstream-Trailer"))
	}
}

func TestReverseProxyCustomErrorHandler(t *testing.T) {
	t.Helper()

	engine := New()
	target, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target: target,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, fmt.Sprintf("proxy failure: %v", err))
		},
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "proxy failure:") {
		t.Fatalf("unexpected body: %q", rr.Body.String())
	}
}

func TestReverseProxyProtocolUpgrade(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headerValuesContainToken(r.Header["Connection"], "Upgrade") {
			t.Errorf("missing upgrade connection header: %#v", r.Header.Values("Connection"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("unexpected upgrade header: %q", r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("backend response writer does not support hijack")
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("backend hijack failed: %v", err)
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if err := brw.Flush(); err != nil {
			t.Fatalf("backend flush failed: %v", err)
		}

		line, err := brw.ReadString('\n')
		if err != nil {
			t.Fatalf("backend read failed: %v", err)
		}
		_, _ = io.WriteString(brw, "echo:"+line)
		if err := brw.Flush(); err != nil {
			t.Fatalf("backend echo flush failed: %v", err)
		}
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/ws", ReverseProxy(ReverseProxyConfig{
		Target: target,
		Via:    "proxy.test",
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	conn, err := net.DialTimeout("tcp", proxy.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	_, err = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: client.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	if err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		t.Fatalf("unexpected status line: %q", statusLine)
	}

	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		t.Fatalf("read headers: %v", err)
	}
	respHeader := http.Header(headers)
	if !strings.EqualFold(respHeader.Get("Upgrade"), "websocket") {
		t.Fatalf("unexpected upgrade response header: %q", respHeader.Get("Upgrade"))
	}
	if !headerValuesContainToken(respHeader.Values("Connection"), "Upgrade") {
		t.Fatalf("unexpected connection response header: %#v", respHeader.Values("Connection"))
	}
	if gotVia := respHeader.Values("Via"); len(gotVia) != 1 || gotVia[0] != "1.1 proxy.test" {
		t.Fatalf("unexpected Via response header: %#v", gotVia)
	}

	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	message, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read tunneled payload: %v", err)
	}
	if message != "echo:ping\n" {
		t.Fatalf("unexpected tunneled payload: %q", message)
	}
}
