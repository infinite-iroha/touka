// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2026 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/netip"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

// ForwardedHeadersPolicy controls how forwarding headers are generated.
// The zero value uses both X-Forwarded-* and RFC 7239 Forwarded headers.
type ForwardedHeadersPolicy int

const (
	ForwardedBoth ForwardedHeadersPolicy = iota
	ForwardedNone
	ForwardedXForwardedOnly
	ForwardedRFC7239Only
)

// BufferPool provides temporary buffers for response body copying.
type BufferPool interface {
	Get() []byte
	Put([]byte)
}

// ReverseProxyConfig configures the reverse proxy handler.
type ReverseProxyConfig struct {
	Target *url.URL
	Targets []string

	LoadBalancing ReverseProxyLoadBalancingConfig
	PassiveHealth ReverseProxyPassiveHealthConfig

	Transport http.RoundTripper
	FlushInterval time.Duration
	BufferPool BufferPool
	AllowH2CUpstream bool

	ModifyRequest func(*http.Request)
	ModifyResponse func(*http.Response) error
	ErrorHandler func(http.ResponseWriter, *http.Request, error)

	ForwardedHeaders ForwardedHeadersPolicy
	ForwardedBy string
	Via string
	PreserveHost bool

	RequestHeaders *HeaderOps
	ResponseHeaders *RespHeaderOps
}

var (
	errReverseProxyNilTarget = errors.New("reverse proxy target is nil")
	errReverseProxyInvalidTarget = errors.New("reverse proxy target must include scheme and host")
	errReverseProxyCopyDone = errors.New("reverse proxy switch protocol copy complete")
	errReverseProxyNoAvailableUpstreams = errors.New("reverse proxy has no available upstreams")
)

type HeaderOps struct {
	Add     map[string][]string
	Set     map[string][]string
	Delete  []string
	Replace map[string][]Replacement
}

type Replacement struct {
	Search       string
	Replace      string
	SearchRegexp string
	re           *regexp.Regexp
}

type RespHeaderOps struct {
	*HeaderOps
	Deferred bool
}

func (ops *HeaderOps) applyToRequest(req *http.Request) {
	if ops == nil {
		return
	}
	ops.applyTo(req.Header, newReverseProxyReplacer(req))
}

func (ops *RespHeaderOps) applyToResponse(hdr http.Header) {
	if ops == nil {
		return
	}
	ops.applyTo(hdr, newReverseProxyReplacerFromHeader(hdr))
}

func (ops *HeaderOps) applyTo(hdr http.Header, repl *reverseProxyReplacer) {
	if ops == nil {
		return
	}
	if repl == nil {
		repl = &reverseProxyReplacer{}
	}
	
	for fieldName, vals := range ops.Add {
		fieldName = repl.Replace(fieldName)
		for _, v := range vals {
			hdr.Add(fieldName, repl.Replace(v))
		}
	}
	
	for fieldName, vals := range ops.Set {
		fieldName = repl.Replace(fieldName)
		hdr.Del(fieldName)
		for _, v := range vals {
			hdr.Add(fieldName, repl.Replace(v))
		}
	}
	
	for _, fieldName := range ops.Delete {
		fieldName = strings.ToLower(repl.Replace(fieldName))
		if fieldName == "*" {
			for k := range hdr {
				hdr.Del(k)
			}
			continue
		}
		
		switch {
		case strings.HasPrefix(fieldName, "*") && strings.HasSuffix(fieldName, "*"):
			pattern := fieldName[1:len(fieldName)-1]
			for k := range hdr {
				if strings.Contains(strings.ToLower(k), pattern) {
					hdr.Del(k)
				}
			}
		case strings.HasPrefix(fieldName, "*"):
			suffix := fieldName[1:]
			for k := range hdr {
				if strings.HasSuffix(strings.ToLower(k), suffix) {
					hdr.Del(k)
				}
			}
		case strings.HasSuffix(fieldName, "*"):
			prefix := fieldName[:len(fieldName)-1]
			for k := range hdr {
				if strings.HasPrefix(strings.ToLower(k), prefix) {
					hdr.Del(k)
				}
			}
		default:
			hdr.Del(fieldName)
		}
	}

	ops.applyReplace(hdr, repl)
}

func (ops *HeaderOps) applyReplace(hdr http.Header, repl *reverseProxyReplacer) {
	if ops == nil || len(ops.Replace) == 0 {
		return
	}
	for fieldName, replacements := range ops.Replace {
		fieldName = http.CanonicalHeaderKey(repl.Replace(fieldName))
		if fieldName == "*" {
			for fn, vals := range hdr {
				for i := range vals {
					for _, r := range replacements {
						hdr[fn][i] = r.apply(vals[i])
					}
				}
			}
			continue
		}
		vals, ok := hdr[fieldName]
		if !ok {
			continue
		}
		for i := range vals {
			for _, r := range replacements {
				hdr[fieldName][i] = r.apply(vals[i])
			}
		}
	}
}

func (r *Replacement) apply(s string) string {
	if r == nil || s == "" {
		return s
	}
	if r.SearchRegexp != "" && r.re != nil {
		return r.re.ReplaceAllString(s, r.Replace)
	}
	if r.Search != "" {
		return strings.ReplaceAll(s, r.Search, r.Replace)
	}
	return s
}

func (ops *HeaderOps) Provision() error {
	if ops == nil {
		return nil
	}
	for fieldName, replacements := range ops.Replace {
		for i, r := range replacements {
			if r.SearchRegexp == "" {
				continue
			}
			if r.Search != "" {
				return fmt.Errorf("replacement %d for header field %q: cannot specify both Search and SearchRegexp", i, fieldName)
			}
			re, err := regexp.Compile(r.SearchRegexp)
			if err != nil {
				return fmt.Errorf("replacement %d for header field %q: %v", i, fieldName, err)
			}
			replacements[i].re = re
		}
	}
	return nil
}

type reverseProxyReplacer struct {
	req *http.Request
}

func newReverseProxyReplacer(req *http.Request) *reverseProxyReplacer {
	return &reverseProxyReplacer{req: req}
}

func newReverseProxyReplacerFromHeader(hdr http.Header) *reverseProxyReplacer {
	return &reverseProxyReplacer{}
}

func (r *reverseProxyReplacer) Replace(s string) string {
	if r == nil || s == "" {
		return s
	}
	return s
}

type reverseProxyHandler struct {
	config      ReverseProxyConfig
	upstreams   []*reverseProxyUpstream
	receivedBy  string
	configError error
	roundRobin  atomic.Uint64
}

var reverseProxyCopyBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

var reverseProxyCandidatePool = sync.Pool{
	New: func() any {
		s := make([]*reverseProxyUpstream, 0, 8)
		return &s
	},
}

type reverseProxyStatusError struct {
	status int
	err    error
}

type reverseProxyExtendedConnectBridge struct {
	body io.ReadCloser
}

type reverseProxyH2ReadWriteCloser struct {
	io.ReadCloser
	ResponseWriter
	controller *http.ResponseController
}

func (rwc *reverseProxyH2ReadWriteCloser) Write(p []byte) (int, error) {
	n, err := rwc.ResponseWriter.Write(p)
	if err != nil {
		return n, err
	}
	if err := rwc.controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return n, err
	}
	return n, nil
}

func (rwc *reverseProxyH2ReadWriteCloser) Close() error {
	if rwc.ReadCloser == nil {
		return nil
	}
	return rwc.ReadCloser.Close()
}

func (e *reverseProxyStatusError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *reverseProxyStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type noopCloseReader struct {
	readCloser io.ReadCloser
	closed     atomic.Bool
}

func (n *noopCloseReader) Read(p []byte) (int, error) {
	if n.closed.Load() {
		return 0, errors.New("reverse proxy read on closed body")
	}
	return n.readCloser.Read(p)
}

func (n *noopCloseReader) Close() error {
	n.closed.Store(true)
	return nil
}

type maxLatencyWriter struct {
	dst     ResponseWriter
	latency time.Duration

	mu           sync.Mutex
	t            *time.Timer
	flushPending bool
}

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	n, err := m.dst.Write(p)
	if m.latency < 0 {
		m.dst.Flush()
		return n, err
	}
	if m.flushPending {
		return n, err
	}
	if m.t == nil {
		m.t = time.AfterFunc(m.latency, m.delayedFlush)
	} else {
		m.t.Reset(m.latency)
	}
	m.flushPending = true
	return n, err
}

func (m *maxLatencyWriter) delayedFlush() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.flushPending {
		return
	}
	m.dst.Flush()
	m.flushPending = false
}

func (m *maxLatencyWriter) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.flushPending = false
	if m.t != nil {
		m.t.Stop()
	}
}

type switchProtocolCopier struct {
	user    io.ReadWriter
	backend io.ReadWriter
}

func (c switchProtocolCopier) copyFromBackend(errc chan<- error) {
	if _, err := io.Copy(c.user, c.backend); err != nil {
		errc <- err
		return
	}
	if cw, ok := c.user.(interface{ CloseWrite() error }); ok {
		errc <- cw.CloseWrite()
		return
	}
	errc <- errReverseProxyCopyDone
}

func (c switchProtocolCopier) copyToBackend(errc chan<- error) {
	if _, err := io.Copy(c.backend, c.user); err != nil {
		errc <- err
		return
	}
	if cw, ok := c.backend.(interface{ CloseWrite() error }); ok {
		errc <- cw.CloseWrite()
		return
	}
	errc <- errReverseProxyCopyDone
}

// ReverseProxy returns a handler that proxies requests to the configured backend.
func ReverseProxy(config ReverseProxyConfig) HandlerFunc {
	proxy := newReverseProxyHandler(config)
	return func(c *Context) {
		proxy.ServeHTTP(c)
	}
}

func newReverseProxyHandler(config ReverseProxyConfig) *reverseProxyHandler {
	proxy := &reverseProxyHandler{
		config:     config,
		receivedBy: reverseProxyReceivedBy(config.Via),
	}

	if config.RequestHeaders != nil {
		if err := config.RequestHeaders.Provision(); err != nil {
			proxy.configError = err
			return proxy
		}
	}
	if config.ResponseHeaders != nil && config.ResponseHeaders.HeaderOps != nil {
		if err := config.ResponseHeaders.HeaderOps.Provision(); err != nil {
			proxy.configError = err
			return proxy
		}
	}

	upstreams, err := buildReverseProxyUpstreams(config)
	if err != nil {
		proxy.configError = err
	} else {
		proxy.upstreams = upstreams
	}

	switch config.ForwardedHeaders {
	case ForwardedBoth, ForwardedNone, ForwardedXForwardedOnly, ForwardedRFC7239Only:
	default:
		proxy.config.ForwardedHeaders = ForwardedBoth
	}
	proxy.config.ForwardedBy = strings.TrimSpace(proxy.config.ForwardedBy)
	if reverseProxyUsesForwardedHeader(proxy.config.ForwardedHeaders) {
		if err := validateReverseProxyForwardedBy(proxy.config.ForwardedBy); err != nil {
			proxy.configError = err
		}
	}
	if proxy.configError == nil {
		if err := validateReverseProxyLBPolicy(proxy.config.LoadBalancing.Policy); err != nil {
			proxy.configError = err
		}
	}

	return proxy
}

func (p *reverseProxyHandler) ServeHTTP(c *Context) {
	defer c.Abort()

	if p.configError != nil {
		p.handleError(c, &reverseProxyStatusError{status: http.StatusInternalServerError, err: p.configError})
		return
	}

	updatedMaxForwards, handledLocally, err := p.handleMaxForwards(c)
	if err != nil {
		p.handleError(c, err)
		return
	}
	if handledLocally {
		return
	}

	ctx, cancel := p.requestContext(c)
	defer cancel()
	attempted := make(map[string]struct{}, len(p.upstreams))
	attempts := 0
	started := time.Now()
	var lastErr error

	for {
		upstream, err := p.selectUpstream(c, attempted)
		if err != nil {
			if lastErr != nil {
				p.handleError(c, lastErr)
				return
			}
			p.handleError(c, &reverseProxyStatusError{status: http.StatusBadGateway, err: err})
			return
		}

		attempts++
		upstream.inFlight.Add(1)
		served, attemptErr, retriable := p.serveUpstreamAttempt(c, ctx, upstream, updatedMaxForwards)
		upstream.inFlight.Add(-1)

		if served {
			return
		}
		if attemptErr != nil {
			lastErr = attemptErr
		}
		if retriable && p.shouldRetryAttempt(c.Request, attempts, started) {
			attempted[upstream.key] = struct{}{}
			if !p.waitRetryInterval(ctx, started) {
				if lastErr != nil {
					p.handleError(c, lastErr)
				}
				return
			}
			continue
		}
		if attemptErr != nil {
			p.handleError(c, attemptErr)
			return
		}
		if lastErr != nil {
			p.handleError(c, lastErr)
			return
		}
		p.handleError(c, &reverseProxyStatusError{status: http.StatusBadGateway, err: errReverseProxyNoAvailableUpstreams})
		return
	}
}

func (p *reverseProxyHandler) serveUpstreamAttempt(c *Context, ctx context.Context, upstream *reverseProxyUpstream, updatedMaxForwards string) (bool, error, bool) {
	outreq, connectWriter, cleanup, err := p.buildOutgoingRequest(c, ctx, upstream, updatedMaxForwards)
	if err != nil {
		return false, err, false
	}
	defer cleanup()

	transport := p.transportForUpstream(outreq, upstream)
	rawWriter := reverseProxyBaseResponseWriter(c.Writer)
	var (
		roundTripMu   sync.Mutex
		roundTripDone bool
	)
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			roundTripMu.Lock()
			defer roundTripMu.Unlock()
			if roundTripDone {
				return nil
			}
			h := c.Writer.Header()
			saved := h.Clone()
			clear(h)
			reverseProxyCopyHeader(h, http.Header(header))
			rawWriter.WriteHeader(code)
			clear(h)
			reverseProxyCopyHeader(h, saved)
			return nil
		},
	}
	outreq = outreq.WithContext(httptrace.WithClientTrace(outreq.Context(), trace))

	res, err := transport.RoundTrip(outreq)
	roundTripMu.Lock()
	roundTripDone = true
	roundTripMu.Unlock()
	if err != nil {
		if reverseProxyShouldCountPassiveFailure(outreq, err) {
			upstream.recordFailure(time.Now(), p.config.PassiveHealth)
		}
		return false, err, true
	}
	if reverseProxyStatusIsUnhealthy(p.config.PassiveHealth, res.StatusCode) {
		upstream.recordFailure(time.Now(), p.config.PassiveHealth)
	}

	if bridge := reverseProxyExtendedConnectBridgeFromContext(outreq.Context()); bridge != nil {
		if res.StatusCode == http.StatusSwitchingProtocols {
			appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)
			if !p.modifyResponse(c, res, outreq) {
				return true, nil, false
			}
			if err := p.handleBridgedExtendedConnectResponse(c, outreq, res, bridge); err != nil {
				return false, err, false
			}
			return true, nil, false
		}
		return false, &reverseProxyStatusError{status: http.StatusBadGateway, err: fmt.Errorf("extended CONNECT backend returned status %d instead of 101", res.StatusCode)}, false
	}

	if outreq.Method == http.MethodConnect && res.StatusCode >= http.StatusOK && res.StatusCode < http.StatusMultipleChoices {
		removeHopByHopHeaders(res.Header)
		res.Header.Del("Content-Length")
		res.Header.Del("Transfer-Encoding")
		res.ContentLength = -1
		res.TransferEncoding = nil
		appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)
		if !p.modifyResponse(c, res, outreq) {
			return true, nil, false
		}
		handleConnect := p.handleConnectResponse
		if reverseProxyIsExtendedConnectRequest(outreq) {
			handleConnect = p.handleExtendedConnectResponse
		}
		if err := handleConnect(c, outreq, res, connectWriter); err != nil {
			return false, err, false
		}
		return true, nil, false
	}

	if res.StatusCode == http.StatusSwitchingProtocols {
		appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)
		if !p.modifyResponse(c, res, outreq) {
			return true, nil, false
		}
		if err := p.handleUpgradeResponse(c, outreq, res); err != nil {
			return false, err, false
		}
		return true, nil, false
	}

	removeHopByHopHeaders(res.Header)
	appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)

	if !p.modifyResponse(c, res, outreq) {
		return true, nil, false
	}

	reverseProxyCopyHeader(c.Writer.Header(), res.Header)

	announcedTrailers := len(res.Trailer)
	if announcedTrailers > 0 {
		trailerKeys := make([]string, 0, len(res.Trailer))
		for key := range res.Trailer {
			trailerKeys = append(trailerKeys, key)
		}
		c.Writer.Header().Add("Trailer", strings.Join(trailerKeys, ", "))
	}

	c.Writer.WriteHeader(res.StatusCode)

	if err := p.copyResponse(c.Writer, res.Body, p.flushInterval(res)); err != nil {
		defer res.Body.Close()
		c.AddError(fmt.Errorf("reverse proxy body copy failed: %w", err))
		p.logf(c, "reverse proxy body copy failed: %v", err)
		if reverseProxyShouldPanicOnCopyError(c.Request) {
			panic(http.ErrAbortHandler)
		}
		return true, nil, false
	}
	res.Body.Close()

	if len(res.Trailer) > 0 {
		c.Writer.Flush()
	}

	if len(res.Trailer) == announcedTrailers {
		reverseProxyCopyHeader(c.Writer.Header(), res.Trailer)
		return true, nil, false
	}

	for key, values := range res.Trailer {
		prefixedKey := http.TrailerPrefix + key
		for _, value := range values {
			c.Writer.Header().Add(prefixedKey, value)
		}
	}
	return true, nil, false
}

func (p *reverseProxyHandler) buildOutgoingRequest(c *Context, ctx context.Context, upstream *reverseProxyUpstream, updatedMaxForwards string) (*http.Request, *io.PipeWriter, func(), error) {
	outreq := c.Request.Clone(ctx)
	bridgeCtx, bridged, err := reverseProxyPrepareExtendedConnectBridge(outreq)
	if err != nil {
		return nil, nil, nil, err
	}
	if bridged {
		outreq = outreq.WithContext(bridgeCtx)
	}
	if outreq.Method == http.MethodConnect || c.Request.ContentLength == 0 {
		outreq.Body = nil
	} else if c.Request.GetBody != nil {
		body, err := c.Request.GetBody()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reverse proxy failed to replay request body: %w", err)
		}
		outreq.Body = body
	} else if outreq.Body != nil {
		outreq.Body = &noopCloseReader{readCloser: outreq.Body}
	}
	if outreq.Header == nil {
		outreq.Header = make(http.Header)
	}
	outreq.Close = false
	var connectWriter *io.PipeWriter
	if outreq.Method == http.MethodConnect && !bridged {
		pipeReader, pipeWriter := io.Pipe()
		outreq.Body = pipeReader
		outreq.ContentLength = -1
		connectWriter = pipeWriter
	}
	cleanup := func() {
		if outreq.Body != nil {
			_ = outreq.Body.Close()
		}
		if connectWriter != nil {
			_ = connectWriter.Close()
		}
	}

	if outreq.Method == http.MethodConnect && !reverseProxyIsExtendedConnectRequest(outreq) {
		if err := rewriteReverseProxyConnectRequest(outreq, upstream.target); err != nil {
			cleanup()
			return nil, nil, nil, err
		}
	} else {
		rewriteReverseProxyURL(outreq, upstream.target)
		if !p.config.PreserveHost {
			outreq.Host = ""
		}
		outreq.URL.RawQuery = cleanReverseProxyQueryParams(outreq.URL.RawQuery)
	}
	if updatedMaxForwards != "" {
		outreq.Header.Set("Max-Forwards", updatedMaxForwards)
	}

	reqUpType := reverseProxyUpgradeType(outreq.Header)
	if reqUpType != "" && !isPrintableASCII(reqUpType) {
		cleanup()
		return nil, nil, nil, &reverseProxyStatusError{
			status: http.StatusBadRequest,
			err:    fmt.Errorf("client tried to switch to invalid protocol %q", reqUpType),
		}
	}

	removeHopByHopHeaders(outreq.Header)
	if headerValuesContainToken(c.Request.Header["Te"], "trailers") {
		outreq.Header.Set("Te", "trailers")
	}
	if reqUpType != "" {
		outreq.Header.Set("Connection", "Upgrade")
		outreq.Header.Set("Upgrade", reqUpType)
	}

	p.addForwardingHeaders(c.Request, outreq)
	appendViaHeader(outreq.Header, reverseProxyViaProtocol(c.Request.ProtoMajor, c.Request.ProtoMinor, c.Request.Proto), p.receivedBy)

	if _, ok := outreq.Header["User-Agent"]; !ok {
		outreq.Header.Set("User-Agent", "")
	}

	if p.config.RequestHeaders != nil {
		p.config.RequestHeaders.applyToRequest(outreq)
	}

	if p.config.ModifyRequest != nil {
		p.config.ModifyRequest(outreq)
	}

	return outreq, connectWriter, cleanup, nil
}

func (p *reverseProxyHandler) transportForUpstream(req *http.Request, upstream *reverseProxyUpstream) http.RoundTripper {
	if p.config.Transport != nil {
		return p.config.Transport
	}
	if reverseProxyExtendedConnectBridgeFromContext(req.Context()) != nil {
		if upstream.bridgeTransport != nil {
			return upstream.bridgeTransport
		}
		return http.DefaultTransport
	}
	if upstream.useH2C && upstream.h2cTransport != nil {
		return upstream.h2cTransport
	}
	if reverseProxyIsExtendedConnectRequest(req) && upstream.extendedConnectTransport != nil {
		return upstream.extendedConnectTransport
	}
	return http.DefaultTransport
}

func (p *reverseProxyHandler) shouldRetryAttempt(req *http.Request, attempts int, started time.Time) bool {
	if req == nil || req.Context().Err() != nil || !reverseProxyCanRetryRequest(req) {
		return false
	}
	lb := p.config.LoadBalancing
	if lb.TryDuration > 0 {
		return time.Since(started) < lb.TryDuration
	}
	return attempts <= lb.Retries
}

func (p *reverseProxyHandler) waitRetryInterval(ctx context.Context, started time.Time) bool {
	interval := p.config.LoadBalancing.TryInterval
	tryDuration := p.config.LoadBalancing.TryDuration
	if tryDuration > 0 && interval == 0 {
		interval = 250 * time.Millisecond
	}
	if tryDuration > 0 {
		remaining := tryDuration - time.Since(started)
		if remaining <= 0 {
			return false
		}
		if interval <= 0 {
			return ctx.Err() == nil
		}
		if interval > remaining {
			return false
		}
	}
	if interval <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *reverseProxyHandler) handleMaxForwards(c *Context) (string, bool, error) {
	if c == nil || c.Request == nil {
		return "", false, nil
	}

	switch c.Request.Method {
	case http.MethodOptions, http.MethodTrace:
	default:
		return "", false, nil
	}

	rawValue := textproto.TrimString(c.Request.Header.Get("Max-Forwards"))
	if rawValue == "" {
		return "", false, nil
	}

	value, err := strconv.Atoi(rawValue)
	if err != nil || value < 0 {
		return "", false, &reverseProxyStatusError{
			status: http.StatusBadRequest,
			err:    fmt.Errorf("invalid Max-Forwards value %q", rawValue),
		}
	}
	if value == 0 {
		switch c.Request.Method {
		case http.MethodTrace:
			return "", true, p.writeLocalTraceResponse(c)
		case http.MethodOptions:
			p.writeLocalOptionsResponse(c)
			return "", true, nil
		}
	}

	return strconv.Itoa(value - 1), false, nil
}

func (p *reverseProxyHandler) writeLocalTraceResponse(c *Context) error {
	if c == nil || c.Request == nil {
		return nil
	}

	traceReq := c.Request.Clone(c.Request.Context())
	traceReq.Body = nil
	traceReq.ContentLength = 0
	traceReq.TransferEncoding = nil
	traceReq.RequestURI = c.Request.RequestURI
	if traceReq.RequestURI == "" && traceReq.URL != nil {
		traceReq.RequestURI = traceReq.URL.RequestURI()
	}
	traceReq.Header = traceReq.Header.Clone()
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Content-Length", "Transfer-Encoding", "Trailer"} {
		traceReq.Header.Del(key)
	}

	dump, err := httputil.DumpRequest(traceReq, false)
	if err != nil {
		return &reverseProxyStatusError{status: http.StatusInternalServerError, err: err}
	}

	c.Writer.Header().Set("Content-Type", "message/http")
	c.Writer.WriteHeader(http.StatusOK)
	_, err = c.Writer.Write(dump)
	return err
}

func (p *reverseProxyHandler) writeLocalOptionsResponse(c *Context) {
	if c == nil {
		return
	}

	if c.engine != nil {
		if c.Request != nil && c.Request.RequestURI != "*" {
			if allow := c.engine.allowedMethodsForPath(routeLookupPath(c.Request), c.allowedMethodsBuf[:0]); len(allow) > 0 {
				c.allowedMethodsBuf = allow[:0]
				allowHeader := c.allowHeaderBuf[:0]
				for i, method := range allow {
					if i > 0 {
						allowHeader = append(allowHeader, ',', ' ')
					}
					allowHeader = append(allowHeader, method...)
				}
				c.allowHeaderBuf = allowHeader[:0]
				c.Writer.Header().Set("Allow", string(allowHeader))
			}
		}
	}
	c.Writer.WriteHeader(http.StatusOK)
}

func (p *reverseProxyHandler) requestContext(c *Context) (context.Context, context.CancelFunc) {
	ctx := c.Request.Context()
	if ctx.Done() != nil {
		return ctx, func() {}
	}

	// Follow the same compatibility path as net/http/httputil.ReverseProxy:
	// request contexts are normally cancelable, but middleware can still replace
	// c.Request with one backed by context.Background/TODO or another context with
	// a nil Done channel. In that case CloseNotifier still provides disconnect
	// propagation for the upstream round trip.
	rawWriter := reverseProxyBaseResponseWriter(c.Writer)
	cn, ok := rawWriter.(http.CloseNotifier)
	if !ok {
		return ctx, func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	notifyChan := cn.CloseNotify()
	go func() {
		select {
		case <-notifyChan:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (p *reverseProxyHandler) addForwardingHeaders(in *http.Request, out *http.Request) {
	if p.config.ForwardedHeaders == ForwardedNone {
		return
	}

	clientIP := reverseProxyClientIP(in.RemoteAddr)
	scheme := reverseProxyRequestScheme(in)
	host := in.Host

	if p.config.ForwardedHeaders == ForwardedBoth || p.config.ForwardedHeaders == ForwardedXForwardedOnly {
		if clientIP != "" {
			appendXForwardedFor(out.Header, clientIP)
		}
		if host != "" {
			if len(out.Header.Values("X-Forwarded-Host")) == 0 {
				out.Header.Set("X-Forwarded-Host", host)
			}
		}
		if scheme != "" {
			if len(out.Header.Values("X-Forwarded-Proto")) == 0 {
				out.Header.Set("X-Forwarded-Proto", scheme)
			}
		}
	}

	if p.config.ForwardedHeaders == ForwardedBoth || p.config.ForwardedHeaders == ForwardedRFC7239Only {
		if forwardedValue := buildForwardedHeaderValue(clientIP, p.config.ForwardedBy, host, scheme); forwardedValue != "" {
			if prior := out.Header.Values("Forwarded"); len(prior) > 0 {
				forwardedValue = strings.Join(prior, ", ") + ", " + forwardedValue
				out.Header.Del("Forwarded")
			}
			out.Header.Add("Forwarded", forwardedValue)
		}
	}
}

func appendXForwardedFor(header http.Header, clientIP string) {
	if clientIP == "" {
		return
	}
	prior := header.Values("X-Forwarded-For")
	if len(prior) == 0 {
		header.Set("X-Forwarded-For", clientIP)
		return
	}
	header.Set("X-Forwarded-For", strings.Join(prior, ", ")+", "+clientIP)
}

func (p *reverseProxyHandler) modifyResponse(c *Context, res *http.Response, req *http.Request) bool {
	if p.config.ResponseHeaders != nil && !p.config.ResponseHeaders.Deferred {
		p.config.ResponseHeaders.applyToResponse(res.Header)
	}

	if p.config.ModifyResponse == nil {
		if p.config.ResponseHeaders != nil && p.config.ResponseHeaders.Deferred {
			p.config.ResponseHeaders.applyToResponse(res.Header)
		}
		return true
	}
	if err := p.config.ModifyResponse(res); err != nil {
		res.Body.Close()
		p.handleError(c, err)
		return false
	}
	if p.config.ResponseHeaders != nil && p.config.ResponseHeaders.Deferred {
		p.config.ResponseHeaders.applyToResponse(res.Header)
	}
	return true
}

func (p *reverseProxyHandler) handleError(c *Context, err error) {
	if err == nil {
		return
	}
	c.AddError(err)
	if c.Writer.IsHijacked() {
		p.logf(c, "reverse proxy error after hijack: %v", err)
		return
	}
	if p.config.ErrorHandler != nil {
		p.config.ErrorHandler(c.Writer, c.Request, err)
		if c.Writer.Written() || c.Writer.IsHijacked() {
			return
		}
	}
	c.ErrorUseHandle(reverseProxyStatusCode(err), err)
}

func (p *reverseProxyHandler) handleUpgradeResponse(c *Context, req *http.Request, res *http.Response) error {
	reqUpType := reverseProxyUpgradeType(req.Header)
	resUpType := reverseProxyUpgradeType(res.Header)
	if reqUpType == "" || resUpType == "" {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("invalid upgrade negotiation: request protocol=%q, response protocol=%q", reqUpType, resUpType),
		}
	}
	if !isPrintableASCII(resUpType) {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("backend tried to switch to invalid protocol %q", resUpType),
		}
	}
	if !strings.EqualFold(reqUpType, resUpType) {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("backend tried to switch protocol %q when %q was requested", resUpType, reqUpType),
		}
	}

	backConn, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    errors.New("backend returned 101 response without writable body"),
		}
	}

	clientConn, brw, err := c.Writer.Hijack()
	if err != nil {
		backConn.Close()
		status := http.StatusBadGateway
		if errors.Is(err, http.ErrNotSupported) {
			status = http.StatusNotImplemented
		}
		return &reverseProxyStatusError{status: status, err: err}
	}

	defer clientConn.Close()
	defer backConn.Close()

	backConnClosed := make(chan struct{})
	go func() {
		select {
		case <-req.Context().Done():
		case <-backConnClosed:
		}
		backConn.Close()
	}()
	defer close(backConnClosed)

	res.Body = nil
	if err := res.Write(brw); err != nil {
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}
	if err := brw.Flush(); err != nil {
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	errc := make(chan error, 2)
	copyer := switchProtocolCopier{user: clientConn, backend: backConn}
	go copyer.copyToBackend(errc)
	go copyer.copyFromBackend(errc)

	firstErr := <-errc
	if firstErr == nil {
		firstErr = <-errc
	}
	if errors.Is(firstErr, errReverseProxyCopyDone) || errors.Is(firstErr, net.ErrClosed) || errors.Is(firstErr, io.EOF) || errors.Is(firstErr, context.Canceled) {
		return nil
	}
	return firstErr
}

func (p *reverseProxyHandler) handleConnectResponse(c *Context, req *http.Request, res *http.Response, backWrite *io.PipeWriter) error {
	if backWrite == nil {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    errors.New("reverse proxy CONNECT tunnel is missing backend writer"),
		}
	}
	backRead := res.Body

	clientConn, brw, err := c.Writer.Hijack()
	if err != nil {
		backRead.Close()
		_ = backWrite.Close()
		status := http.StatusBadGateway
		if errors.Is(err, http.ErrNotSupported) {
			status = http.StatusNotImplemented
		}
		return &reverseProxyStatusError{status: status, err: err}
	}

	defer clientConn.Close()
	defer backRead.Close()
	defer backWrite.Close()

	backConnClosed := make(chan struct{})
	go func() {
		select {
		case <-req.Context().Done():
		case <-backConnClosed:
		}
		backRead.Close()
		_ = backWrite.Close()
	}()
	defer close(backConnClosed)

	res.Body = nil
	if err := res.Write(brw); err != nil {
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}
	if err := brw.Flush(); err != nil {
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	errc := make(chan error, 2)
	go func() {
		if _, err := io.Copy(clientConn, backRead); err != nil {
			errc <- err
			return
		}
		if cw, ok := clientConn.(interface{ CloseWrite() error }); ok {
			errc <- cw.CloseWrite()
			return
		}
		errc <- errReverseProxyCopyDone
	}()
	go func() {
		if _, err := io.Copy(backWrite, clientConn); err != nil {
			errc <- err
			return
		}
		errc <- backWrite.Close()
	}()

	firstErr := <-errc
	if firstErr == nil {
		firstErr = <-errc
	}
	if errors.Is(firstErr, errReverseProxyCopyDone) || errors.Is(firstErr, net.ErrClosed) || errors.Is(firstErr, io.EOF) || errors.Is(firstErr, context.Canceled) {
		return nil
	}
	return firstErr
}

func (p *reverseProxyHandler) handleBridgedExtendedConnectResponse(c *Context, req *http.Request, res *http.Response, bridge *reverseProxyExtendedConnectBridge) error {
	if c == nil || c.Request == nil {
		res.Body.Close()
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: errors.New("extended CONNECT bridge requires a valid request context")}
	}
	backConn, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    errors.New("backend returned bridged websocket response without writable body"),
		}
	}

	controller := http.NewResponseController(reverseProxyBaseResponseWriter(c.Writer))
	if err := controller.EnableFullDuplex(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		backConn.Close()
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	responseHeader := c.Writer.Header()
	reverseProxyCopyHeader(responseHeader, res.Header)
	removeHopByHopHeaders(responseHeader)
	responseHeader.Del("Sec-WebSocket-Accept")
	c.Writer.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		backConn.Close()
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	conn := &reverseProxyH2ReadWriteCloser{ReadCloser: bridge.body, ResponseWriter: c.Writer, controller: controller}

	var closeOnce sync.Once
	closeTunnel := func() {
		closeOnce.Do(func() {
			_ = conn.Close()
			_ = backConn.Close()
		})
	}
	go func() {
		<-req.Context().Done()
		closeTunnel()
	}()

	errc := make(chan error, 2)
	copyer := switchProtocolCopier{user: conn, backend: backConn}
	go copyer.copyToBackend(errc)
	go copyer.copyFromBackend(errc)

	var firstErr error
	for range 2 {
		err := <-errc
		if reverseProxyIsBenignTunnelError(err) {
			continue
		}
		if firstErr == nil {
			firstErr = err
			closeTunnel()
		}
	}
	closeTunnel()
	if reverseProxyIsBenignTunnelError(firstErr) {
		return nil
	}
	return firstErr
}

func (p *reverseProxyHandler) handleExtendedConnectResponse(c *Context, req *http.Request, res *http.Response, backWrite *io.PipeWriter) error {
	if c == nil || c.Request == nil {
		res.Body.Close()
		if backWrite != nil {
			_ = backWrite.Close()
		}
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: errors.New("extended CONNECT requires a valid request context")}
	}
	if backWrite == nil {
		res.Body.Close()
		return &reverseProxyStatusError{
			status: http.StatusBadGateway,
			err:    errors.New("reverse proxy extended CONNECT tunnel is missing backend writer"),
		}
	}

	controller := http.NewResponseController(reverseProxyBaseResponseWriter(c.Writer))
	if err := controller.EnableFullDuplex(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		res.Body.Close()
		_ = backWrite.Close()
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	reverseProxyCopyHeader(c.Writer.Header(), res.Header)
	c.Writer.WriteHeader(res.StatusCode)
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		res.Body.Close()
		_ = backWrite.Close()
		return &reverseProxyStatusError{status: http.StatusBadGateway, err: err}
	}

	var closeOnce sync.Once
	closeTunnel := func() {
		closeOnce.Do(func() {
			_ = c.Request.Body.Close()
			_ = backWrite.Close()
			_ = res.Body.Close()
		})
	}
	go func() {
		<-req.Context().Done()
		closeTunnel()
	}()

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(backWrite, c.Request.Body)
		closeErr := backWrite.Close()
		if err != nil && !reverseProxyIsBenignTunnelError(err) {
			errc <- err
			return
		}
		errc <- closeErr
	}()
	go func() {
		copyErr := p.copyResponse(c.Writer, res.Body, -1)
		closeErr := res.Body.Close()
		if copyErr != nil {
			errc <- copyErr
			return
		}
		errc <- closeErr
	}()

	var firstErr error
	for range 2 {
		err := <-errc
		if reverseProxyIsBenignTunnelError(err) {
			continue
		}
		if firstErr == nil {
			firstErr = err
			closeTunnel()
		}
	}
	closeTunnel()
	if reverseProxyIsBenignTunnelError(firstErr) {
		return nil
	}

	return firstErr

}

func (p *reverseProxyHandler) flushInterval(res *http.Response) time.Duration {
	if baseType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); baseType == "text/event-stream" {
		return -1
	}
	if res.ContentLength == -1 {
		return -1
	}
	return p.config.FlushInterval
}

func (p *reverseProxyHandler) copyResponse(dst ResponseWriter, src io.Reader, flushInterval time.Duration) error {
	var writer io.Writer = dst

	if flushInterval != 0 {
		mlw := &maxLatencyWriter{dst: dst, latency: flushInterval}
		defer mlw.stop()
		mlw.flushPending = true
		mlw.t = time.AfterFunc(flushInterval, mlw.delayedFlush)
		writer = mlw
	}

	var buf []byte
	if p.config.BufferPool != nil {
		buf = p.config.BufferPool.Get()
		defer p.config.BufferPool.Put(buf)
	} else {
		bufp := reverseProxyCopyBufferPool.Get().(*[]byte)
		buf = *bufp
		defer reverseProxyCopyBufferPool.Put(bufp)
	}
	_, err := p.copyBuffer(writer, src, buf)
	return err
}

func (p *reverseProxyHandler) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 32*1024)
	}

	var written int64
	for {
		nr, rerr := src.Read(buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !reverseProxyIsBenignTunnelError(rerr) {
			p.logf(nil, "reverse proxy read error during body copy: %v", rerr)
		}
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if werr != nil {
				return written, werr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return written, nil
			}
			return written, rerr
		}
	}
}

func (p *reverseProxyHandler) logf(c *Context, format string, args ...any) {
	if c != nil {
		if logger := c.GetLogger(); logger != nil {
			logger.Errorf(format, args...)
			return
		}
	}
	log.Printf(format, args...)
}

func reverseProxyStatusCode(err error) int {
	var statusErr *reverseProxyStatusError
	if errors.As(err, &statusErr) && statusErr.status > 0 {
		return statusErr.status
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func validateReverseProxyTarget(target *url.URL) error {
	if target == nil {
		return errReverseProxyNilTarget
	}
	if target.Scheme == "" || target.Host == "" {
		return errReverseProxyInvalidTarget
	}
	return nil
}

func buildReverseProxyUpstreams(config ReverseProxyConfig) ([]*reverseProxyUpstream, error) {
	if config.Target != nil && len(config.Targets) > 0 {
		return nil, errors.New("reverse proxy Target and Targets cannot be used together")
	}

	targets := make([]*url.URL, 0, max(1, len(config.Targets)))
	if config.Target != nil {
		target := cloneReverseProxyURL(config.Target)
		normalizeReverseProxyTarget(target)
		if err := validateReverseProxyTarget(target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	for i, rawTarget := range config.Targets {
		trimmed := strings.TrimSpace(rawTarget)
		if trimmed == "" {
			return nil, fmt.Errorf("reverse proxy target at index %d is empty", i)
		}
		target, err := url.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("reverse proxy target at index %d is invalid: %w", i, err)
		}
		normalizeReverseProxyTarget(target)
		if err := validateReverseProxyTarget(target); err != nil {
			return nil, fmt.Errorf("reverse proxy target at index %d is invalid: %w", i, err)
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return nil, errReverseProxyNilTarget
	}

	upstreams := make([]*reverseProxyUpstream, 0, len(targets))
	for i, target := range targets {
		useH2C := strings.EqualFold(target.Scheme, "h2c")
		if useH2C {
			target = cloneReverseProxyURL(target)
			target.Scheme = "http"
		}
		upstream := &reverseProxyUpstream{
			key:    fmt.Sprintf("%d:%s", i, target.String()),
			target: target,
			index:  i,
			useH2C: useH2C || config.AllowH2CUpstream,
		}
		if config.Transport == nil {
			upstream.extendedConnectTransport = newHTTP2ExtendedConnectTransport()
			upstream.bridgeTransport = newHTTP1BridgeTransport()
			if upstream.useH2C {
				upstream.h2cTransport = newH2CTransport()
			}
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, nil
}

func validateReverseProxyForwardedBy(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if !isValidForwardedNodeIdentifier(trimmed) {
		return fmt.Errorf("reverse proxy ForwardedBy must be an RFC 7239 node identifier, got %q", value)
	}
	return nil
}

func normalizeReverseProxyTarget(target *url.URL) {
	switch strings.ToLower(target.Scheme) {
	case "ws":
		target.Scheme = "http"
	case "wss":
		target.Scheme = "https"
	}
}

func cloneReverseProxyURL(target *url.URL) *url.URL {
	if target == nil {
		return nil
	}
	clone := *target
	return &clone
}

func reverseProxyReceivedBy(configValue string) string {
	trimmed := strings.TrimSpace(configValue)
	if trimmed != "" {
		return trimmed
	}
	return "touka-engine"
}

func reverseProxyClientIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return addrPort.Addr().String()
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
			return addr.String()
		}
		return host
	}
	if addr, err := netip.ParseAddr(remoteAddr); err == nil {
		return addr.String()
	}
	return remoteAddr
}

func reverseProxyRequestScheme(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.TLS != nil {
		return "https"
	}
	if req.URL != nil {
		scheme := strings.ToLower(req.URL.Scheme)
		if scheme != "" {
			return scheme
		}
	}
	return "http"
}

func buildForwardedHeaderValue(clientIP, by, host, scheme string) string {
	pairs := make([]string, 0, 4)
	if by != "" {
		pairs = append(pairs, "by="+formatForwardedParameterValue(by))
	}
	if clientIP != "" {
		pairs = append(pairs, "for="+formatForwardedFor(clientIP))
	}
	if host != "" {
		pairs = append(pairs, "host="+formatForwardedParameterValue(host))
	}
	if scheme != "" {
		pairs = append(pairs, "proto="+formatForwardedParameterValue(strings.ToLower(scheme)))
	}
	if len(pairs) == 0 {
		return ""
	}
	return strings.Join(pairs, ";")
}

func reverseProxyUsesForwardedHeader(policy ForwardedHeadersPolicy) bool {
	return policy == ForwardedBoth || policy == ForwardedRFC7239Only
}

func reverseProxyPrepareExtendedConnectBridge(req *http.Request) (context.Context, bool, error) {
	if req == nil {
		return context.Background(), false, nil
	}
	protocol := reverseProxyExtendedConnectProtocol(req)
	if req.Method != http.MethodConnect || protocol == "" || !strings.EqualFold(protocol, "websocket") {
		return req.Context(), false, nil
	}

	bridge := &reverseProxyExtendedConnectBridge{body: req.Body}
	ctx := context.WithValue(req.Context(), reverseProxyExtendedConnectBridge{}, bridge)
	req.Header.Del(":protocol")
	req.Method = http.MethodGet
	req.Body = http.NoBody
	req.ContentLength = 0
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	key, err := reverseProxyGenerateWebSocketKey()
	if err != nil {
		return nil, false, fmt.Errorf("reverse proxy failed to generate websocket key: %w", err)
	}
	req.Header.Set("Sec-WebSocket-Key", key)
	return ctx, true, nil
}

func reverseProxyExtendedConnectBridgeFromContext(ctx context.Context) *reverseProxyExtendedConnectBridge {
	if ctx == nil {
		return nil
	}
	bridge, _ := ctx.Value(reverseProxyExtendedConnectBridge{}).(*reverseProxyExtendedConnectBridge)
	return bridge
}

func reverseProxyGenerateWebSocketKey() (string, error) {
	key := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func reverseProxyIsExtendedConnectRequest(req *http.Request) bool {
	return reverseProxyExtendedConnectProtocol(req) != ""
}

func reverseProxyExtendedConnectProtocol(req *http.Request) string {
	if req == nil || req.Method != http.MethodConnect || req.Header == nil {
		return ""
	}
	return textproto.TrimString(req.Header.Get(":protocol"))
}

func isValidForwardedNodeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "[") {
		closing := strings.IndexByte(value, ']')
		if closing <= 1 {
			return false
		}
		addr, err := netip.ParseAddr(value[1:closing])
		if err != nil || !addr.Is6() {
			return false
		}
		if closing == len(value)-1 {
			return true
		}
		if value[closing+1] != ':' {
			return false
		}
		return isValidForwardedNodePort(value[closing+2:])
	}

	host, port, hasPort := strings.Cut(value, ":")
	if hasPort {
		switch {
		case host == "unknown", isValidForwardedObfuscatedIdentifier(host):
			return isValidForwardedNodePort(port)
		default:
			addr, err := netip.ParseAddr(host)
			return err == nil && addr.Is4() && isValidForwardedNodePort(port)
		}
	}

	if value == "unknown" || isValidForwardedObfuscatedIdentifier(value) {
		return true
	}
	addr, err := netip.ParseAddr(value)
	return err == nil && addr.Is4()
}

func isValidForwardedNodePort(value string) bool {
	if value == "" {
		return false
	}
	if isValidForwardedObfuscatedIdentifier(value) {
		return true
	}
	if len(value) > 5 {
		return false
	}
	port, err := strconv.Atoi(value)
	return err == nil && port > 0 && port <= 65535
}

func isValidForwardedObfuscatedIdentifier(value string) bool {
	if len(value) < 2 || value[0] != '_' {
		return false
	}
	for i := 1; i < len(value); i++ {
		b := value[i]
		if (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			continue
		}
		switch b {
		case '.', '_', '-':
			continue
		default:
			return false
		}
	}
	return true
}

func formatForwardedFor(clientIP string) string {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return formatForwardedParameterValue(clientIP)
	}
	if addr.Is6() {
		return quoteForwardedString("[" + addr.String() + "]")
	}
	return addr.String()
}

func formatForwardedParameterValue(value string) string {
	if isToken(value) {
		return value
	}
	return quoteForwardedString(value)
}

func quoteForwardedString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(value) + `"`
}

func isToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isTokenChar(value[i]) {
			return false
		}
	}
	return true
}

func isTokenChar(b byte) bool {
	if b >= '0' && b <= '9' {
		return true
	}
	if b >= 'A' && b <= 'Z' {
		return true
	}
	if b >= 'a' && b <= 'z' {
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func appendViaHeader(header http.Header, protocol, receivedBy string) {
	if header == nil || receivedBy == "" {
		return
	}
	if protocol == "" {
		protocol = "1.1"
	}
	header.Add("Via", protocol+" "+receivedBy)
}

func reverseProxyViaProtocol(major, minor int, raw string) string {
	if major > 0 {
		return strconv.Itoa(major) + "." + strconv.Itoa(minor)
	}
	if after, ok := strings.CutPrefix(raw, "HTTP/"); ok {
		return after
	}
	return raw
}

func rewriteReverseProxyURL(req *http.Request, target *url.URL) {
	targetQuery := target.RawQuery
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path, req.URL.RawPath = joinReverseProxyURLPath(target, req.URL)
	if targetQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = targetQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
	}
}

func rewriteReverseProxyConnectRequest(req *http.Request, target *url.URL) error {
	connectTarget, err := reverseProxyConnectTarget(target)
	if err != nil {
		return &reverseProxyStatusError{status: http.StatusBadRequest, err: err}
	}
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = ""
	req.URL.RawPath = ""
	req.URL.RawQuery = ""
	req.URL.Opaque = connectTarget
	req.Host = connectTarget
	return nil
}

func reverseProxyConnectTarget(target *url.URL) (string, error) {
	if target == nil {
		return "", errReverseProxyNilTarget
	}
	host := target.Hostname()
	if host == "" {
		return "", errReverseProxyInvalidTarget
	}
	port := target.Port()
	if port == "" {
		switch strings.ToLower(target.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("reverse proxy CONNECT target requires a supported scheme, got %q", target.Scheme)
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return "", fmt.Errorf("reverse proxy CONNECT target has invalid port %q", port)
	}
	return net.JoinHostPort(host, port), nil
}

func joinReverseProxyURLPath(base, incoming *url.URL) (string, string) {
	if base.RawPath == "" && incoming.RawPath == "" {
		return reverseProxySingleJoiningSlash(base.Path, incoming.Path), ""
	}

	baseEscaped := base.EscapedPath()
	incomingEscaped := incoming.EscapedPath()

	baseSlash := strings.HasSuffix(baseEscaped, "/")
	incomingSlash := strings.HasPrefix(incomingEscaped, "/")

	switch {
	case baseSlash && incomingSlash:
		return base.Path + incoming.Path[1:], baseEscaped + incomingEscaped[1:]
	case !baseSlash && !incomingSlash:
		return base.Path + "/" + incoming.Path, baseEscaped + "/" + incomingEscaped
	default:
		return base.Path + incoming.Path, baseEscaped + incomingEscaped
	}
}

func reverseProxySingleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

func reverseProxyCopyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

var reverseProxyHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func removeHopByHopHeaders(header http.Header) {
	for _, connectionValue := range header["Connection"] {
		for token := range strings.SplitSeq(connectionValue, ",") {
			trimmed := textproto.TrimString(token)
			if trimmed != "" {
				header.Del(trimmed)
			}
		}
	}
	for _, hopHeader := range reverseProxyHopHeaders {
		header.Del(hopHeader)
	}
}

func reverseProxyUpgradeType(header http.Header) string {
	if !headerValuesContainToken(header["Connection"], "Upgrade") {
		return ""
	}
	return header.Get("Upgrade")
}

func headerValuesContainToken(values []string, token string) bool {
	if token == "" {
		return false
	}
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(textproto.TrimString(part), token) {
				return true
			}
		}
	}
	return false
}

func cleanReverseProxyQueryParams(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	// Normalize the outgoing query string so the proxy and upstream do not see
	// different semantics for non-standard separators or malformed pairs.
	// This can change the exact textual form of the original query and may drop
	// parts that net/url rejects, but it keeps proxy-chain parsing behavior more
	// consistent and reduces parameter-smuggling ambiguity.
	values, _ := url.ParseQuery(rawQuery)
	return values.Encode()
}

func reverseProxyShouldPanicOnCopyError(req *http.Request) bool {
	return req != nil && req.Context().Value(http.ServerContextKey) != nil
}

func reverseProxyCanRetryRequest(req *http.Request) bool {
	if req == nil || req.Method == http.MethodConnect || reverseProxyUpgradeType(req.Header) != "" || !reverseProxyMethodIsSafe(req.Method) {
		return false
	}
	if req.Body == nil || req.ContentLength == 0 {
		return true
	}
	return req.GetBody != nil
}

func reverseProxyShouldCountPassiveFailure(req *http.Request, err error) bool {
	if err == nil || reverseProxyIsBenignTunnelError(err) {
		return false
	}
	if req != nil && req.Context().Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func reverseProxyMethodIsSafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func reverseProxyIsBenignTunnelError(err error) bool {
	return err == nil || errors.Is(err, errReverseProxyCopyDone) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, context.Canceled) || errors.Is(err, http.ErrAbortHandler) || reverseProxyIsClosedBodyError(err)
}

func reverseProxyIsClosedBodyError(err error) bool {
	if err == nil {
		return false
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) && streamErr.Code == http2.ErrCodeCancel {
		return true
	}
	switch err.Error() {
	case "body closed by handler", "http2: response body closed", "response body closed":
		return true
	default:
		return false
	}
}

func reverseProxyBaseResponseWriter(writer ResponseWriter) http.ResponseWriter {
	return UnwrapResponseWriter(writer)
}

func isPrintableASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}
