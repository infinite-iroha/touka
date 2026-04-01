package touka

import (
	"bufio"
	"context"
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
		ForwardedBy:      "_proxy-node",
		Via:              "proxy.test",
	}))

	req := httptest.NewRequest(http.MethodGet, "http://client.example/api/ping?bad=1;smuggle=2&q=2", nil)
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
	if !strings.Contains(got.Forwarded, "by=_proxy-node") {
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

func TestReverseProxyRejectsInvalidForwardedBy(t *testing.T) {
	t.Helper()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target:           mustParseURL(t, "http://example.com"),
		ForwardedHeaders: ForwardedBoth,
		ForwardedBy:      "proxy-node",
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
}

func TestReverseProxyForwardedByTrimsWhitespace(t *testing.T) {
	t.Helper()

	forwardedCh := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedCh <- r.Header.Get("Forwarded")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target:           mustParseURL(t, backend.URL),
		ForwardedHeaders: ForwardedBoth,
		ForwardedBy:      " _proxy-node ",
	}))

	req := httptest.NewRequest(http.MethodGet, "http://client.example/proxy", nil)
	req.RemoteAddr = "198.51.100.10:4567"
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rr.Code)
	}

	select {
	case forwarded := <-forwardedCh:
		if !strings.Contains(forwarded, "by=_proxy-node") {
			t.Fatalf("unexpected Forwarded header: %q", forwarded)
		}
		if strings.Contains(forwarded, `by=" _proxy-node "`) {
			t.Fatalf("forwarded header should not preserve surrounding whitespace: %q", forwarded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend Forwarded header")
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

func TestReverseProxyTimeoutReturnsGatewayTimeout(t *testing.T) {
	t.Helper()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, "http://example.com"),
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("unexpected status: %d", rr.Code)
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

func TestReverseProxyUpgradeNeedsHijacker(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_ = brw.Flush()
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/ws", ReverseProxy(ReverseProxyConfig{Target: mustParseURL(t, backend.URL)}))

	req := httptest.NewRequest(http.MethodGet, "http://client.example/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
}

func TestReverseProxyMaxForwardsTraceHandledLocally(t *testing.T) {
	t.Helper()

	called := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	engine := New()
	engine.Handle(http.MethodTrace, "/trace", ReverseProxy(ReverseProxyConfig{Target: mustParseURL(t, backend.URL)}))

	req := httptest.NewRequest(http.MethodTrace, "http://client.example/trace", nil)
	req.RequestURI = "/trace"
	req.Header.Set("Max-Forwards", "0")
	req.Header.Set("Authorization", "secret")
	req.Header.Set("Cookie", "a=b")
	req.Header.Set("Forwarded", "for=192.0.2.1")

	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	resp := rr.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "message/http" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if !strings.Contains(string(body), "TRACE /trace HTTP/1.1") {
		t.Fatalf("trace body missing request line: %q", string(body))
	}
	if strings.Contains(string(body), "Authorization:") {
		t.Fatalf("trace body leaked authorization header: %q", string(body))
	}
	if strings.Contains(string(body), "Cookie:") {
		t.Fatalf("trace body leaked cookie header: %q", string(body))
	}
	if strings.Contains(string(body), "Forwarded:") {
		t.Fatalf("trace body leaked forwarded header: %q", string(body))
	}

	select {
	case <-called:
		t.Fatal("backend should not be called when Max-Forwards is zero")
	default:
	}
}

func TestReverseProxyMaxForwardsTraceDecrementsBeforeForwarding(t *testing.T) {
	t.Helper()

	maxForwardsCh := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		maxForwardsCh <- r.Header.Get("Max-Forwards")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	engine := New()
	engine.Handle(http.MethodTrace, "/trace", ReverseProxy(ReverseProxyConfig{Target: mustParseURL(t, backend.URL)}))

	req := httptest.NewRequest(http.MethodTrace, "http://client.example/trace", nil)
	req.Header.Set("Max-Forwards", "2")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", rr.Code)
	}

	select {
	case got := <-maxForwardsCh:
		if got != "1" {
			t.Fatalf("unexpected Max-Forwards header: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend Max-Forwards")
	}
}

func TestReverseProxyMaxForwardsOptionsHandledLocally(t *testing.T) {
	t.Helper()

	called := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/proxy", func(c *Context) { c.Status(http.StatusNoContent) })
	engine.OPTIONS("/proxy", ReverseProxy(ReverseProxyConfig{Target: mustParseURL(t, backend.URL)}))

	req := httptest.NewRequest(http.MethodOptions, "http://client.example/proxy", nil)
	req.Header.Set("Max-Forwards", "0")
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	allow := rr.Header().Get("Allow")
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodOptions) {
		t.Fatalf("unexpected Allow header: %q", allow)
	}

	select {
	case <-called:
		t.Fatal("backend should not be called when Max-Forwards is zero")
	default:
	}
}

func TestEngineDoesNotTreatOptionsAsteriskAsSlashRoute(t *testing.T) {
	t.Helper()

	engine := New()
	engine.OPTIONS("/", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodOptions, "http://client.example/", nil)
	req.RequestURI = "*"
	req.URL.Path = ""
	req.URL.RawPath = ""
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status for OPTIONS *: %d", rr.Code)
	}
}

func TestReverseProxyConnectTunnel(t *testing.T) {
	t.Helper()

	backendAddr := ""
	errCh := make(chan error, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			errCh <- fmt.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if got, want := r.RequestURI, backendAddr; got != want {
			errCh <- fmt.Errorf("unexpected CONNECT target %q, want %q", got, want)
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

		_, _ = io.WriteString(brw, "HTTP/1.1 200 Connection Established\r\nVia: 1.1 upstream\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("backend flush failed: %w", err)
			return
		}

		line, err := brw.ReadString('\n')
		if err != nil {
			errCh <- fmt.Errorf("backend read failed: %w", err)
			return
		}
		_, _ = io.WriteString(brw, strings.ToUpper(line))
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("backend write failed: %w", err)
			return
		}
	}))
	defer backend.Close()
	backendAddr = strings.TrimPrefix(backend.URL, "http://")

	engine := New()
	engine.Handle(http.MethodConnect, "/:authority", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, backend.URL),
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

	_, err = fmt.Fprintf(conn, "CONNECT origin.example:443 HTTP/1.1\r\nHost: origin.example:443\r\n\r\n")
	if err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("unexpected status line: %q", statusLine)
	}

	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		t.Fatalf("read headers: %v", err)
	}
	respHeader := http.Header(headers)
	if got := respHeader.Get("Content-Length"); got != "" {
		t.Fatalf("CONNECT response should not include Content-Length, got %q", got)
	}
	if got := respHeader.Get("Transfer-Encoding"); got != "" {
		t.Fatalf("CONNECT response should not include Transfer-Encoding, got %q", got)
	}
	if gotVia := respHeader.Values("Via"); len(gotVia) != 2 || gotVia[0] != "1.1 upstream" || gotVia[1] != "1.1 proxy.test" {
		t.Fatalf("unexpected Via response header: %#v", gotVia)
	}

	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	message, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read tunneled payload: %v", err)
	}
	if message != "PING\n" {
		t.Fatalf("unexpected tunneled payload: %q", message)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyConnectNeedsHijacker(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("backend response writer does not support hijack")
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("backend hijack failed: %v", err)
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = brw.Flush()
	}))
	defer backend.Close()

	engine := New()
	engine.Handle(http.MethodConnect, "/tunnel", ReverseProxy(ReverseProxyConfig{Target: mustParseURL(t, backend.URL)}))

	req := httptest.NewRequest(http.MethodConnect, "http://client.example/tunnel", nil)
	req.URL.Path = "/tunnel"
	req.RequestURI = "/tunnel"
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
}

func TestReverseProxyAbortsStreamingCopyFailure(t *testing.T) {
	t.Helper()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, "http://example.com"),
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/plain"},
				},
				Body:          &failingReadCloser{chunks: []string{"ok"}, err: errors.New("boom")},
				ContentLength: -1,
				Request:       req,
			}, nil
		}),
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	resp, err := proxy.Client().Get(proxy.URL + "/proxy")
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err == nil {
		t.Fatal("expected body read to fail after upstream copy error")
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

type failingReadCloser struct {
	chunks []string
	err    error
}

func (r *failingReadCloser) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, r.err
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

func (r *failingReadCloser) Close() error {
	return nil
}
