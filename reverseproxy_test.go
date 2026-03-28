package touka

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
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

	gotCh := make(chan backendRequestSnapshot, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCh <- backendRequestSnapshot{
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

	var got backendRequestSnapshot
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend snapshot")
	}

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

func TestReverseProxyDefaultViaFallback(t *testing.T) {
	t.Helper()

	viaCh := make(chan []string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaCh <- append([]string(nil), r.Header.Values("Via")...)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{Target: target}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rr.Code)
	}

	select {
	case via := <-viaCh:
		if len(via) != 1 || via[0] != "1.1 touka-engine" {
			t.Fatalf("unexpected default Via header: %#v", via)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend Via header")
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

func TestReverseProxyUnannouncedTrailerForwarding(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(http.TrailerPrefix+"X-Unannounced-Trailer", "later")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "streamed")
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/trailers", ReverseProxy(ReverseProxyConfig{Target: target}))

	rr := PerformRequest(engine, http.MethodGet, "/trailers", nil, nil)
	resp := rr.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if string(body) != "streamed" {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if got := resp.Trailer.Get("X-Unannounced-Trailer"); got != "later" {
		t.Fatalf("unexpected unannounced trailer: %q", got)
	}
}

func TestReverseProxyProtocolUpgrade(t *testing.T) {
	t.Helper()

	errCh := make(chan error, 8)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headerValuesContainToken(r.Header["Connection"], "Upgrade") {
			errCh <- fmt.Errorf("missing upgrade connection header: %#v", r.Header.Values("Connection"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected upgrade header: %q", r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("backend response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("backend hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("backend flush failed: %w", err)
			return
		}

		line, err := brw.ReadString('\n')
		if err != nil {
			errCh <- fmt.Errorf("backend read failed: %w", err)
			return
		}
		_, _ = io.WriteString(brw, "echo:"+line)
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("backend echo flush failed: %w", err)
			return
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

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyRejectsEmptyUpgradeProtocol(t *testing.T) {
	t.Helper()

	errCh := make(chan error, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("backend response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("backend hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("backend flush failed: %w", err)
			return
		}
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/ws", ReverseProxy(ReverseProxyConfig{Target: target}))

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

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyRestoresHeadersAfter1xx(t *testing.T) {
	t.Helper()

	type oneXXInfo struct {
		code   int
		header http.Header
	}

	backendTraceCh := make(chan struct{}, 1)
	oneXXCh := make(chan oneXXInfo, 1)

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(req.Context())
		if trace == nil || trace.Got1xxResponse == nil {
			return nil, errors.New("missing Got1xxResponse trace")
		}
		backendTraceCh <- struct{}{}
		if err := trace.Got1xxResponse(http.StatusEarlyHints, textproto.MIMEHeader{"Link": {"</style.css>; rel=preload; as=style"}}); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/plain"},
			},
			Body:          io.NopCloser(strings.NewReader("ok")),
			ContentLength: 2,
			Request:       req,
		}, nil
	})

	engine := New()
	engine.Use(func(c *Context) {
		c.Writer.Header().Set("X-Request-Id", "req-123")
		c.Next()
	})
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target:    mustParseURL(t, "http://example.com"),
		Transport: transport,
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	client := proxy.Client()
	req, err := http.NewRequest(http.MethodGet, proxy.URL+"/proxy", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			oneXXCh <- oneXXInfo{code: code, header: http.Header(header).Clone()}
			return nil
		},
	}))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case <-backendTraceCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected proxy transport 1xx trace to be invoked")
	}

	var oneXX oneXXInfo
	select {
	case oneXX = <-oneXXCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected client to receive 1xx response")
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if got := resp.Header.Get("X-Request-Id"); got != "req-123" {
		t.Fatalf("final response lost preserved header: %q", got)
	}
	if got := resp.Header.Get("Link"); got != "" {
		t.Fatalf("interim 1xx header leaked into final response: %q", got)
	}
	if oneXX.code != http.StatusEarlyHints {
		t.Fatalf("unexpected interim status: %d", oneXX.code)
	}
	if got := oneXX.header.Get("Link"); got != "</style.css>; rel=preload; as=style" {
		t.Fatalf("unexpected interim Link header: %q", got)
	}
	if got := oneXX.header.Get("X-Request-Id"); got != "" {
		t.Fatalf("final-only header leaked into interim response: %q", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}
