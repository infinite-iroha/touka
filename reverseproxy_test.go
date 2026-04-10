package touka

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
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
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d body=%q", resp.StatusCode, string(body))
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

func TestReverseProxyRejectsConflictingTargetConfig(t *testing.T) {
	t.Helper()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target:  mustParseURL(t, "http://example.com"),
		Targets: []string{"http://example.net"},
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
}

func TestReverseProxyTargetsRoundRobinPreservesFullURLTargets(t *testing.T) {
	t.Helper()

	type snapshot struct {
		Path     string
		RawQuery string
	}

	backendOneCh := make(chan snapshot, 1)
	backendOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendOneCh <- snapshot{Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		_, _ = io.WriteString(w, "one")
	}))
	defer backendOne.Close()

	backendTwoCh := make(chan snapshot, 1)
	backendTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendTwoCh <- snapshot{Path: r.URL.Path, RawQuery: r.URL.RawQuery}
		_, _ = io.WriteString(w, "two")
	}))
	defer backendTwo.Close()

	engine := New()
	engine.GET("/api/*path", ReverseProxy(ReverseProxyConfig{
		Targets: []string{
			backendOne.URL + "/one?from=one",
			backendTwo.URL + "/two?from=two",
		},
		LoadBalancing: ReverseProxyLoadBalancingConfig{Policy: LBRoundRobin()},
	}))

	first := PerformRequest(engine, http.MethodGet, "/api/ping?q=1", nil, nil)
	if first.Code != http.StatusOK || first.Body.String() != "one" {
		t.Fatalf("unexpected first response: code=%d body=%q", first.Code, first.Body.String())
	}
	second := PerformRequest(engine, http.MethodGet, "/api/pong?q=2", nil, nil)
	if second.Code != http.StatusOK || second.Body.String() != "two" {
		t.Fatalf("unexpected second response: code=%d body=%q", second.Code, second.Body.String())
	}

	select {
	case got := <-backendOneCh:
		if got.Path != "/one/api/ping" || got.RawQuery != "from=one&q=1" {
			t.Fatalf("unexpected first upstream request: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	select {
	case got := <-backendTwoCh:
		if got.Path != "/two/api/pong" || got.RawQuery != "from=two&q=2" {
			t.Fatalf("unexpected second upstream request: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second upstream request")
	}
}

func TestReverseProxyHeaderPolicyFallbackAndStickiness(t *testing.T) {
	t.Helper()

	backendOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one")
	}))
	defer backendOne.Close()

	backendTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "two")
	}))
	defer backendTwo.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backendOne.URL, backendTwo.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBHeader("X-Upstream", LBFirst()),
		},
	}))

	fallbackResp := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if fallbackResp.Code != http.StatusOK || fallbackResp.Body.String() != "one" {
		t.Fatalf("unexpected fallback response: code=%d body=%q", fallbackResp.Code, fallbackResp.Body.String())
	}

	headers := http.Header{"X-Upstream": {"tenant-a"}}
	firstSticky := PerformRequest(engine, http.MethodGet, "/proxy", nil, headers)
	secondSticky := PerformRequest(engine, http.MethodGet, "/proxy", nil, headers)
	if firstSticky.Code != http.StatusOK || secondSticky.Code != http.StatusOK {
		t.Fatalf("unexpected sticky statuses: %d %d", firstSticky.Code, secondSticky.Code)
	}
	if firstSticky.Body.String() != secondSticky.Body.String() {
		t.Fatalf("header policy should be sticky, got %q and %q", firstSticky.Body.String(), secondSticky.Body.String())
	}
}

func TestReverseProxyQueryPolicyFallbackAndStickiness(t *testing.T) {
	t.Helper()

	backendOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one")
	}))
	defer backendOne.Close()

	backendTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "two")
	}))
	defer backendTwo.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backendOne.URL, backendTwo.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBQuery("tenant", LBFirst()),
		},
	}))

	fallbackResp := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if fallbackResp.Code != http.StatusOK || fallbackResp.Body.String() != "one" {
		t.Fatalf("unexpected fallback response: code=%d body=%q", fallbackResp.Code, fallbackResp.Body.String())
	}

	firstSticky := PerformRequest(engine, http.MethodGet, "/proxy?tenant=a", nil, nil)
	secondSticky := PerformRequest(engine, http.MethodGet, "/proxy?tenant=a", nil, nil)
	if firstSticky.Code != http.StatusOK || secondSticky.Code != http.StatusOK {
		t.Fatalf("unexpected sticky statuses: %d %d", firstSticky.Code, secondSticky.Code)
	}
	if firstSticky.Body.String() != secondSticky.Body.String() {
		t.Fatalf("query policy should be sticky, got %q and %q", firstSticky.Body.String(), secondSticky.Body.String())
	}
}

func TestReverseProxyClientIPHashUsesParsedClientIP(t *testing.T) {
	t.Helper()

	backendOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one")
	}))
	defer backendOne.Close()

	backendTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "two")
	}))
	defer backendTwo.Close()

	engine := New()
	engine.SetRemoteIPHeaders([]string{"CF-Connecting-IP"})
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backendOne.URL, backendTwo.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBClientIPHash(),
		},
	}))

	reqOne := httptest.NewRequest(http.MethodGet, "http://client.example/proxy", nil)
	reqOne.RemoteAddr = "10.0.0.1:1234"
	reqOne.Header.Set("CF-Connecting-IP", "203.0.113.10")
	rrOne := httptest.NewRecorder()
	engine.ServeHTTP(rrOne, reqOne)

	reqTwo := httptest.NewRequest(http.MethodGet, "http://client.example/proxy", nil)
	reqTwo.RemoteAddr = "10.0.0.2:5678"
	reqTwo.Header.Set("CF-Connecting-IP", "203.0.113.10")
	rrTwo := httptest.NewRecorder()
	engine.ServeHTTP(rrTwo, reqTwo)

	if rrOne.Code != http.StatusOK || rrTwo.Code != http.StatusOK {
		t.Fatalf("unexpected statuses: %d %d", rrOne.Code, rrTwo.Code)
	}
	if rrOne.Body.String() != rrTwo.Body.String() {
		t.Fatalf("client IP hash should use parsed client IP, got %q and %q", rrOne.Body.String(), rrTwo.Body.String())
	}
}

func TestReverseProxyRetriesSafeRequestsAcrossTargets(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{"http://127.0.0.1:1", backend.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy:  LBFirst(),
			Retries: 1,
		},
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("unexpected retry response: code=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestReverseProxyModifyRequestRunsPerRetryAttempt(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Attempt"))
	}))
	defer backend.Close()

	var attempts atomic.Int64
	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{"http://127.0.0.1:1", backend.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy:  LBFirst(),
			Retries: 1,
		},
		ModifyRequest: func(req *http.Request) {
			req.Header.Set("X-Attempt", strconv.FormatInt(attempts.Add(1), 10))
		},
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if rr.Body.String() != "2" {
		t.Fatalf("ModifyRequest should run again for the retry attempt, got %q", rr.Body.String())
	}
}

func TestReverseProxyDoesNotRetryUnsafeRequestsAcrossTargets(t *testing.T) {
	t.Helper()

	backendCalls := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls <- struct{}{}
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	engine := New()
	engine.POST("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{"http://127.0.0.1:1", backend.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy:  LBFirst(),
			Retries: 1,
		},
	}))

	rr := PerformRequest(engine, http.MethodPost, "/proxy", strings.NewReader("payload"), nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", rr.Code)
	}

	select {
	case <-backendCalls:
		t.Fatal("unsafe POST request should not be retried to the next upstream")
	default:
	}
}

func TestReverseProxyLeastConnPrefersLessBusyUpstream(t *testing.T) {
	t.Helper()

	backendOneStarted := make(chan struct{}, 1)
	releaseBackendOne := make(chan struct{})
	backendOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendOneStarted <- struct{}{}
		<-releaseBackendOne
		_, _ = io.WriteString(w, "one")
	}))
	defer backendOne.Close()

	backendTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "two")
	}))
	defer backendTwo.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backendOne.URL, backendTwo.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBLeastConn(),
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()
	client := proxy.Client()
	client.Timeout = 5 * time.Second

	firstRespCh := make(chan string, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		resp, err := client.Get(proxy.URL + "/proxy")
		if err != nil {
			firstErrCh <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstRespCh <- string(body)
	}()

	select {
	case <-backendOneStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first backend request")
	}

	secondResp, err := client.Get(proxy.URL + "/proxy")
	if err != nil {
		close(releaseBackendOne)
		t.Fatalf("second request failed: %v", err)
	}
	secondBody, err := io.ReadAll(secondResp.Body)
	_ = secondResp.Body.Close()
	if err != nil {
		close(releaseBackendOne)
		t.Fatalf("read second response: %v", err)
	}
	if string(secondBody) != "two" {
		close(releaseBackendOne)
		t.Fatalf("least_conn should pick the less busy upstream, got %q", string(secondBody))
	}

	close(releaseBackendOne)
	select {
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case body := <-firstRespCh:
		if body != "one" {
			t.Fatalf("unexpected first response body: %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response body")
	}
}

func TestReverseProxyPassiveHealthSkipsUnhealthyTargetsOnLaterRequests(t *testing.T) {
	t.Helper()

	primaryCalls := make(chan struct{}, 4)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls <- struct{}{}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "primary down")
	}))
	defer primary.Close()

	secondaryCalls := make(chan struct{}, 4)
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryCalls <- struct{}{}
		_, _ = io.WriteString(w, "secondary up")
	}))
	defer secondary.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{primary.URL, secondary.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBFirst(),
		},
		PassiveHealth: ReverseProxyPassiveHealthConfig{
			FailDuration:    time.Minute,
			UnhealthyStatus: []int{http.StatusServiceUnavailable},
		},
	}))

	first := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if first.Code != http.StatusServiceUnavailable || first.Body.String() != "primary down" {
		t.Fatalf("unexpected first response: code=%d body=%q", first.Code, first.Body.String())
	}
	second := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if second.Code != http.StatusOK || second.Body.String() != "secondary up" {
		t.Fatalf("unexpected second response: code=%d body=%q", second.Code, second.Body.String())
	}

	select {
	case <-primaryCalls:
	default:
		t.Fatal("expected primary to receive the first request")
	}
	select {
	case <-secondaryCalls:
	default:
		t.Fatal("expected secondary to receive the second request")
	}
	select {
	case <-primaryCalls:
		t.Fatal("primary should not receive the second request while unhealthy")
	default:
	}
}

func TestReverseProxyPassiveHealthIgnoresClientCancellation(t *testing.T) {
	t.Helper()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backend.URL},
		PassiveHealth: ReverseProxyPassiveHealthConfig{
			FailDuration: time.Minute,
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/proxy", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	client := proxy.Client()
	respCh := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		respCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend request")
	}
	cancel()
	close(release)
	select {
	case <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled request to finish")
	}

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("healthy backend should remain selectable after client cancellation, got code=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestReverseProxyTryDurationPreventsLateRetry(t *testing.T) {
	t.Helper()

	backendCalls := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls <- struct{}{}
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Targets: []string{"http://127.0.0.1:1", backend.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy:      LBFirst(),
			Retries:     3,
			TryDuration: 100 * time.Millisecond,
			TryInterval: 250 * time.Millisecond,
		},
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", rr.Code)
	}

	select {
	case <-backendCalls:
		t.Fatal("retry budget should expire before the next upstream attempt")
	default:
	}
}

func TestReverseProxyAllowH2CUpstream(t *testing.T) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen h2c upstream: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Proto", r.Proto)
		_, _ = io.WriteString(w, "ok")
	})}
	server.Protocols = new(http.Protocols)
	server.Protocols.SetUnencryptedHTTP2(true)
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		<-errCh
	}()

	engine := New()
	engine.GET("/proxy", ReverseProxy(ReverseProxyConfig{
		Target:           mustParseURL(t, "http://"+listener.Addr().String()),
		AllowH2CUpstream: true,
	}))

	rr := PerformRequest(engine, http.MethodGet, "/proxy", nil, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("unexpected response: code=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Upstream-Proto"); got != "HTTP/2.0" {
		t.Fatalf("expected h2c upstream proto, got %q", got)
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

func TestReverseProxyH2ReadWriteCloserWriteReturnsWrittenCountOnFlushError(t *testing.T) {
	t.Helper()

	flushErr := errors.New("flush failed")
	writer := &flushErrorResponseWriter{flushErr: flushErr}
	conn := &reverseProxyH2ReadWriteCloser{
		ReadCloser:     io.NopCloser(strings.NewReader("")),
		ResponseWriter: writer,
		controller:     http.NewResponseController(reverseProxyBaseResponseWriter(writer)),
	}

	n, err := conn.Write([]byte("ping"))
	if n != len("ping") {
		t.Fatalf("unexpected bytes written: %d", n)
	}
	if !errors.Is(err, flushErr) {
		t.Fatalf("unexpected write error: %v", err)
	}
	if got := writer.body.String(); got != "ping" {
		t.Fatalf("unexpected buffered body: %q", got)
	}
}

func TestReverseProxyExtendedConnectBridgeKeyGenerationFailureReturnsError(t *testing.T) {
	t.Helper()

	transportCalled := atomic.Bool{}
	entropyErr := errors.New("entropy source unavailable")
	originalReader := crand.Reader
	crand.Reader = errorReader{err: entropyErr}
	t.Cleanup(func() {
		crand.Reader = originalReader
	})

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, "http://example.com"),
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			transportCalled.Store(true)
			return nil, errors.New("unexpected round trip")
		}),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.WriteHeader(reverseProxyStatusCode(err))
			_, _ = io.WriteString(w, err.Error())
		},
	}))

	headers := make(http.Header)
	headers.Set(":protocol", "websocket")
	rr := PerformRequest(engine, http.MethodConnect, "/ws", nil, headers)

	if transportCalled.Load() {
		t.Fatal("transport should not be called when websocket key generation fails")
	}
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "reverse proxy failed to generate websocket key") || !strings.Contains(body, entropyErr.Error()) {
		t.Fatalf("unexpected error body: %q", body)
	}
}

func TestHTTP2TransportBuildersDoNotPanicWhenDefaultTransportIsCustom(t *testing.T) {
	t.Helper()

	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected round trip")
	})
	t.Cleanup(func() {
		http.DefaultTransport = originalDefaultTransport
	})

	assertTransport := func(name string, rt http.RoundTripper, check func(*http.Transport)) {
		t.Helper()
		transport, ok := rt.(*http.Transport)
		if !ok {
			t.Fatalf("%s returned %T, want *http.Transport", name, rt)
		}
		check(transport)
	}

	assertTransport("newHTTP2ExtendedConnectTransport", newHTTP2ExtendedConnectTransport(), func(transport *http.Transport) {
		if transport.Protocols == nil || !transport.Protocols.HTTP1() || !transport.Protocols.HTTP2() {
			t.Fatalf("unexpected protocols for extended connect transport: %#v", transport.Protocols)
		}
	})
	assertTransport("newHTTP1BridgeTransportWithTLSConfig", newHTTP1BridgeTransportWithTLSConfig(nil), func(transport *http.Transport) {
		if transport.Protocols == nil || !transport.Protocols.HTTP1() || transport.Protocols.HTTP2() || transport.Protocols.UnencryptedHTTP2() {
			t.Fatalf("unexpected protocols for bridge transport: %#v", transport.Protocols)
		}
		if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.NextProtos) != 1 || transport.TLSClientConfig.NextProtos[0] != "http/1.1" {
			t.Fatalf("unexpected TLS next protos for bridge transport: %#v", transport.TLSClientConfig)
		}
	})
	assertTransport("newH2CTransport", newH2CTransport(), func(transport *http.Transport) {
		if transport.Protocols == nil || !transport.Protocols.UnencryptedHTTP2() || transport.Protocols.HTTP1() || transport.Protocols.HTTP2() {
			t.Fatalf("unexpected protocols for h2c transport: %#v", transport.Protocols)
		}
	})
}

func TestNewHTTP1BridgeTransportWithTLSConfigClonesInput(t *testing.T) {
	t.Helper()

	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	rt := newHTTP1BridgeTransportWithTLSConfig(tlsConfig)
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type: %T", rt)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLS client config")
	}
	if transport.TLSClientConfig == tlsConfig {
		t.Fatal("expected bridge transport to clone TLS config")
	}
	if len(tlsConfig.NextProtos) != 0 {
		t.Fatalf("input TLS config was mutated: %#v", tlsConfig.NextProtos)
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("unexpected transport NextProtos: %#v", got)
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

func TestEngineHandlesOptionsAsteriskLocally(t *testing.T) {
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

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status for OPTIONS *: %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Length"); got != "0" {
		t.Fatalf("unexpected Content-Length header: %q", got)
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

func TestReverseProxyHTTP2ExtendedConnect(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	errCh := make(chan error, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errCh <- fmt.Errorf("unexpected upstream method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get(":protocol"); got != "" {
			errCh <- fmt.Errorf("unexpected upstream :protocol header: %q", got)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !headerValuesContainToken(r.Header.Values("Connection"), "Upgrade") {
			errCh <- fmt.Errorf("unexpected upstream Connection header: %#v", r.Header.Values("Connection"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected upstream Upgrade header: %q", r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Sec-WebSocket-Key"); got == "" {
			errCh <- errors.New("missing upstream Sec-WebSocket-Key header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.URL.Path; got != "/ws" {
			errCh <- fmt.Errorf("unexpected upstream path: %q", got)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("upstream response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("upstream hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade, X-Hop-Token\r\nX-Hop-Token: hidden\r\nSec-WebSocket-Accept: ignored\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("upstream flush failed: %w", err)
			return
		}

		line, err := brw.ReadString('\n')
		if err != nil {
			errCh <- fmt.Errorf("read tunneled request body failed: %w", err)
			return
		}
		if _, err := io.WriteString(brw, "echo:"+line); err != nil {
			errCh <- fmt.Errorf("write tunneled response body failed: %w", err)
			return
		}
		_ = brw.Flush()
	}))
	defer upstream.Close()

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target:    mustParseURL(t, upstream.URL),
		Transport: newHTTP1BridgeTransportWithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
		Via:       "proxy.test",
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, proxy.URL+"/ws", pr)
	if err != nil {
		t.Fatalf("new CONNECT request: %v", err)
	}
	req.Header.Set(":protocol", "websocket")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip extended CONNECT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Upgrade"); got != "" {
		t.Fatalf("bridged extended CONNECT response should not expose Upgrade header, got %q", got)
	}
	if got := resp.Header.Get("X-Hop-Token"); got != "" {
		t.Fatalf("bridged extended CONNECT response should not expose hop-by-hop token header, got %q", got)
	}
	if gotVia := resp.Header.Values("Via"); len(gotVia) != 1 || gotVia[0] != "1.1 proxy.test" {
		t.Fatalf("unexpected Via response header: %#v", gotVia)
	}

	if _, err := io.WriteString(pw, "ping\n"); err != nil {
		t.Fatalf("write tunneled request body: %v", err)
	}
	message, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read tunneled response body: %v", err)
	}
	if message != "echo:ping\n" {
		t.Fatalf("unexpected tunneled response body: %q", message)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close tunneled request body: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyHTTP2ExtendedConnectBridgeClosesBackendOnce(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	closeCalls := atomic.Int32{}
	backendReadDone := make(chan struct{}, 1)
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, fmt.Errorf("unexpected upstream method: %s", req.Method)
		}
		var respondOnce sync.Once
		var backend *countingReadWriteCloser
		backend = &countingReadWriteCloser{
			readDataCh:    make(chan []byte, 1),
			closeCalls:    &closeCalls,
			closeWriteErr: nil,
			afterWrite: func() {
				respondOnce.Do(func() {
					backendReadDone <- struct{}{}
					backend.readDataCh <- []byte("echo:ping\n")
					close(backend.readDataCh)
				})
			},
		}
		return &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Header: http.Header{
				"Connection":           []string{"Upgrade"},
				"Upgrade":              []string{"websocket"},
				"Sec-WebSocket-Accept": []string{"ignored"},
			},
			Body:    backend,
			Request: req,
		}, nil
	})

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target:    mustParseURL(t, "http://example.com"),
		Transport: transport,
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	clientTransport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer clientTransport.CloseIdleConnections()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, proxy.URL+"/ws", pr)
	if err != nil {
		t.Fatalf("new CONNECT request: %v", err)
	}
	req.Header.Set(":protocol", "websocket")

	resp, err := clientTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip extended CONNECT: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if _, err := io.WriteString(pw, "ping\n"); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("write tunneled request body: %v", err)
	}
	select {
	case <-backendReadDone:
	case <-time.After(2 * time.Second):
		_ = resp.Body.Close()
		t.Fatal("backend did not receive tunneled request body")
	}
	message, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		_ = resp.Body.Close()
		t.Fatalf("read tunneled response body: %v", err)
	}
	if message != "echo:ping\n" {
		_ = resp.Body.Close()
		t.Fatalf("unexpected tunneled response body: %q", message)
	}
	if err := pw.Close(); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("close tunneled request body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if closeCalls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("expected backend connection to close exactly once, got %d", got)
	}
}

func TestReverseProxyHTTP2ExtendedConnectForcesHTTP1ToTLSUpstream(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	errCh := make(chan error, 4)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			errCh <- fmt.Errorf("expected bridged upstream protocol HTTP/1.x, got %s", r.Proto)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet {
			errCh <- fmt.Errorf("unexpected upstream method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !headerValuesContainToken(r.Header.Values("Connection"), "Upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected websocket bridge headers: Connection=%#v Upgrade=%q", r.Header.Values("Connection"), r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("upstream response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("upstream hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("upstream flush failed: %w", err)
			return
		}

		line, err := brw.ReadString('\n')
		if err != nil {
			errCh <- fmt.Errorf("read tunneled request body failed: %w", err)
			return
		}
		if _, err := io.WriteString(brw, "echo:"+line); err != nil {
			errCh <- fmt.Errorf("write tunneled response body failed: %w", err)
			return
		}
		_ = brw.Flush()
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target:    mustParseURL(t, upstream.URL),
		Transport: newHTTP1BridgeTransportWithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
		Via:       "proxy.test",
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, proxy.URL+"/ws", pr)
	if err != nil {
		t.Fatalf("new CONNECT request: %v", err)
	}
	req.Header.Set(":protocol", "websocket")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip extended CONNECT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d body=%q", resp.StatusCode, string(body))
	}
	if _, err := io.WriteString(pw, "ping\n"); err != nil {
		t.Fatalf("write tunneled request body: %v", err)
	}
	message, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read tunneled response body: %v", err)
	}
	if message != "echo:ping\n" {
		t.Fatalf("unexpected tunneled response body: %q", message)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close tunneled request body: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyHTTP2ExtendedConnectTargetsRoundRobin(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	errCh := make(chan error, 8)
	newBackend := func(name string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				errCh <- fmt.Errorf("%s unexpected upstream method: %s", name, r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if got := r.Header.Get(":protocol"); got != "" {
				errCh <- fmt.Errorf("%s unexpected upstream :protocol header: %q", name, got)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !headerValuesContainToken(r.Header.Values("Connection"), "Upgrade") {
				errCh <- fmt.Errorf("%s unexpected upstream Connection header: %#v", name, r.Header.Values("Connection"))
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				errCh <- fmt.Errorf("%s unexpected upstream Upgrade header: %q", name, r.Header.Get("Upgrade"))
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if got := r.Header.Get("Sec-WebSocket-Key"); got == "" {
				errCh <- fmt.Errorf("%s missing upstream Sec-WebSocket-Key header", name)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			hj, ok := w.(http.Hijacker)
			if !ok {
				errCh <- fmt.Errorf("%s upstream response writer does not support hijack", name)
				return
			}
			conn, brw, err := hj.Hijack()
			if err != nil {
				errCh <- fmt.Errorf("%s upstream hijack failed: %w", name, err)
				return
			}
			defer conn.Close()

			_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			if err := brw.Flush(); err != nil {
				errCh <- fmt.Errorf("%s upstream flush failed: %w", name, err)
				return
			}

			line, err := brw.ReadString('\n')
			if err != nil {
				errCh <- fmt.Errorf("%s read tunneled request body failed: %w", name, err)
				return
			}
			if _, err := io.WriteString(brw, name+":"+line); err != nil {
				errCh <- fmt.Errorf("%s write tunneled response body failed: %w", name, err)
				return
			}
			_ = brw.Flush()
		}))
		return server
	}

	backendOne := newBackend("one")
	defer backendOne.Close()
	backendTwo := newBackend("two")
	defer backendTwo.Close()

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Targets: []string{backendOne.URL, backendTwo.URL},
		LoadBalancing: ReverseProxyLoadBalancingConfig{
			Policy: LBRoundRobin(),
		},
		Via: "proxy.test",
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()

	doRequest := func(payload string) string {
		pr, pw := io.Pipe()
		req, err := http.NewRequest(http.MethodConnect, proxy.URL+"/ws", pr)
		if err != nil {
			t.Fatalf("new CONNECT request: %v", err)
		}
		req.Header.Set(":protocol", "websocket")

		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("round trip extended CONNECT: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("unexpected status: %d body=%q", resp.StatusCode, string(body))
		}
		if _, err := io.WriteString(pw, payload+"\n"); err != nil {
			t.Fatalf("write tunneled request body: %v", err)
		}
		if err := pw.Close(); err != nil {
			t.Fatalf("close tunneled request body: %v", err)
		}
		message, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err != nil {
			t.Fatalf("read tunneled response body: %v", err)
		}
		return message
	}

	if got := doRequest("ping"); got != "one:ping\n" {
		t.Fatalf("unexpected first tunneled response: %q", got)
	}
	if got := doRequest("pong"); got != "two:pong\n" {
		t.Fatalf("unexpected second tunneled response: %q", got)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyHTTP2ExtendedConnectAllowsHalfClose(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	errCh := make(chan error, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errCh <- fmt.Errorf("unexpected upstream method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("upstream response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("upstream hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		if err := brw.Flush(); err != nil {
			errCh <- fmt.Errorf("upstream flush failed: %w", err)
			return
		}

		reader := bufio.NewReader(brw)
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- fmt.Errorf("read tunneled request body failed: %w", err)
			return
		}
		if _, err := io.WriteString(brw, "ack:"+line); err != nil {
			errCh <- fmt.Errorf("write immediate tunneled response failed: %w", err)
			return
		}
		_ = brw.Flush()

		if _, err := io.Copy(io.Discard, reader); err != nil {
			errCh <- fmt.Errorf("wait for request half-close failed: %w", err)
			return
		}
		if _, err := io.WriteString(brw, "after-close\n"); err != nil {
			errCh <- fmt.Errorf("write post-close tunneled response failed: %w", err)
			return
		}
		_ = brw.Flush()
	}))
	defer upstream.Close()

	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, upstream.URL),
		Via:    "proxy.test",
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, proxy.URL+"/ws", pr)
	if err != nil {
		t.Fatalf("new CONNECT request: %v", err)
	}
	req.Header.Set(":protocol", "websocket")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip extended CONNECT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d body=%q", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	if _, err := io.WriteString(pw, "ping\n"); err != nil {
		t.Fatalf("write tunneled request body: %v", err)
	}
	message, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read immediate tunneled response: %v", err)
	}
	if message != "ack:ping\n" {
		t.Fatalf("unexpected immediate tunneled response: %q", message)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close tunneled request body: %v", err)
	}

	message, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read post-close tunneled response: %v", err)
	}
	if message != "after-close\n" {
		t.Fatalf("unexpected post-close tunneled response: %q", message)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func TestReverseProxyHTTP2ExtendedConnectCancelDoesNotTriggerProxyError(t *testing.T) {
	t.Helper()

	enableHTTP2ExtendedConnectProtocol()

	errCh := make(chan error, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			errCh <- fmt.Errorf("unexpected upstream method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			errCh <- errors.New("upstream response writer does not support hijack")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			errCh <- fmt.Errorf("upstream hijack failed: %w", err)
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()

		<-r.Context().Done()
	}))
	defer upstream.Close()

	proxyErrCh := make(chan error, 1)
	engine := New()
	engine.Handle(http.MethodConnect, "/ws", ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, upstream.URL),
		Via:    "proxy.test",
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			select {
			case proxyErrCh <- err:
			default:
			}
		},
	}))

	proxy := httptest.NewUnstartedServer(engine)
	proxy.EnableHTTP2 = true
	if err := configureHTTP2ExtendedConnectServer(proxy.Config); err != nil {
		t.Fatalf("configure proxy HTTP/2 server: %v", err)
	}
	proxy.StartTLS()
	defer proxy.Close()

	transport := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, proxy.URL+"/ws", pr)
	if err != nil {
		t.Fatalf("new CONNECT request: %v", err)
	}
	req.Header.Set(":protocol", "websocket")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip extended CONNECT: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status: %d body=%q", resp.StatusCode, string(body))
	}

	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(pw, strings.Repeat("x", 1<<20))
		writeErrCh <- err
	}()
	time.Sleep(50 * time.Millisecond)

	cancel()
	if err := pw.CloseWithError(context.Canceled); err != nil {
		t.Fatalf("close request body with cancellation: %v", err)
	}
	select {
	case <-writeErrCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request body writer to unblock")
	}

	select {
	case err := <-proxyErrCh:
		t.Fatalf("proxy error handler should not be called on cancellation, got: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
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

type flushErrorResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	written  bool
	flushErr error
}

func (w *flushErrorResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *flushErrorResponseWriter) WriteHeader(statusCode int) {
	if w.written {
		return
	}
	w.status = statusCode
	w.written = true
}

func (w *flushErrorResponseWriter) Write(p []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

func (w *flushErrorResponseWriter) Flush() {}

func (w *flushErrorResponseWriter) FlushError() error {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.flushErr
}

func (w *flushErrorResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

func (w *flushErrorResponseWriter) Status() int {
	return w.status
}

func (w *flushErrorResponseWriter) Size() int {
	return w.body.Len()
}

func (w *flushErrorResponseWriter) Written() bool {
	return w.written
}

func (w *flushErrorResponseWriter) IsHijacked() bool {
	return false
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type countingReadWriteCloser struct {
	readData      []byte
	readDataCh    chan []byte
	writeBuf      bytes.Buffer
	closeCalls    *atomic.Int32
	closeWriteErr error
	afterWrite    func()
}

func (r *countingReadWriteCloser) Read(p []byte) (int, error) {
	if len(r.readData) == 0 && r.readDataCh != nil {
		data, ok := <-r.readDataCh
		if !ok {
			return 0, io.EOF
		}
		r.readData = data
	}
	if len(r.readData) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.readData)
	r.readData = r.readData[n:]
	return n, nil
}

func (r *countingReadWriteCloser) Write(p []byte) (int, error) {
	n, err := r.writeBuf.Write(p)
	if err == nil && r.afterWrite != nil {
		r.afterWrite()
	}
	return n, err
}

func (r *countingReadWriteCloser) Close() error {
	if r.closeCalls != nil {
		r.closeCalls.Add(1)
	}
	return nil
}

func (r *countingReadWriteCloser) CloseWrite() error {
	return r.closeWriteErr
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
