// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2026 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

	Transport     http.RoundTripper
	FlushInterval time.Duration
	BufferPool    BufferPool

	ModifyRequest  func(*http.Request)
	ModifyResponse func(*http.Response) error
	ErrorHandler   func(http.ResponseWriter, *http.Request, error)

	ForwardedHeaders ForwardedHeadersPolicy
	ForwardedBy      string
	Via              string
	PreserveHost     bool
}

var (
	errReverseProxyNilTarget     = errors.New("reverse proxy target is nil")
	errReverseProxyInvalidTarget = errors.New("reverse proxy target must include scheme and host")
	errReverseProxyCopyDone      = errors.New("reverse proxy switch protocol copy complete")
)

type reverseProxyHandler struct {
	config      ReverseProxyConfig
	target      *url.URL
	receivedBy  string
	configError error
}

type reverseProxyStatusError struct {
	status int
	err    error
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
	target := cloneReverseProxyURL(config.Target)
	if target != nil {
		normalizeReverseProxyTarget(target)
	}

	proxy := &reverseProxyHandler{
		config:     config,
		target:     target,
		receivedBy: reverseProxyReceivedBy(config.Via),
	}

	if err := validateReverseProxyTarget(target); err != nil {
		proxy.configError = err
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

	return proxy
}

func (p *reverseProxyHandler) ServeHTTP(c *Context) {
	defer c.Abort()

	if p.configError != nil {
		p.handleError(c, &reverseProxyStatusError{status: http.StatusInternalServerError, err: p.configError})
		return
	}

	transport := p.config.Transport
	if transport == nil {
		transport = http.DefaultTransport
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

	outreq := c.Request.Clone(ctx)
	if outreq.Method == http.MethodConnect || c.Request.ContentLength == 0 {
		outreq.Body = nil
	}
	if outreq.Body != nil {
		outreq.Body = &noopCloseReader{readCloser: outreq.Body}
		defer outreq.Body.Close()
	}
	if outreq.Header == nil {
		outreq.Header = make(http.Header)
	}
	outreq.Close = false
	var connectWriter *io.PipeWriter
	defer func() {
		if connectWriter != nil {
			_ = connectWriter.Close()
		}
	}()
	if outreq.Method == http.MethodConnect {
		pipeReader, pipeWriter := io.Pipe()
		outreq.Body = pipeReader
		outreq.ContentLength = -1
		defer outreq.Body.Close()
		connectWriter = pipeWriter
	}

	if outreq.Method == http.MethodConnect {
		if err := rewriteReverseProxyConnectRequest(outreq, p.target); err != nil {
			p.handleError(c, err)
			return
		}
	} else {
		rewriteReverseProxyURL(outreq, p.target)
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
		p.handleError(c, &reverseProxyStatusError{
			status: http.StatusBadRequest,
			err:    fmt.Errorf("client tried to switch to invalid protocol %q", reqUpType),
		})
		return
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

	if p.config.ModifyRequest != nil {
		p.config.ModifyRequest(outreq)
	}

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
		p.handleError(c, err)
		return
	}

	if outreq.Method == http.MethodConnect && res.StatusCode >= http.StatusOK && res.StatusCode < http.StatusMultipleChoices {
		removeHopByHopHeaders(res.Header)
		res.Header.Del("Content-Length")
		res.Header.Del("Transfer-Encoding")
		res.ContentLength = -1
		res.TransferEncoding = nil
		appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)
		if !p.modifyResponse(c, res, outreq) {
			return
		}
		if err := p.handleConnectResponse(c, outreq, res, connectWriter); err != nil {
			p.handleError(c, err)
		}
		connectWriter = nil
		return
	}

	if res.StatusCode == http.StatusSwitchingProtocols {
		appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)
		if !p.modifyResponse(c, res, outreq) {
			return
		}
		if err := p.handleUpgradeResponse(c, outreq, res); err != nil {
			p.handleError(c, err)
		}
		return
	}

	removeHopByHopHeaders(res.Header)
	appendViaHeader(res.Header, reverseProxyViaProtocol(res.ProtoMajor, res.ProtoMinor, res.Proto), p.receivedBy)

	if !p.modifyResponse(c, res, outreq) {
		return
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
		return
	}
	res.Body.Close()

	if len(res.Trailer) > 0 {
		c.Writer.Flush()
	}

	// Keep the stdlib-compatible fallback here.
	// If the backend only exposes additional trailer keys after the body has been
	// fully read, the trailer map can grow and those values must be written using
	// the TrailerPrefix form instead of the pre-announced bare header keys.
	if len(res.Trailer) == announcedTrailers {
		reverseProxyCopyHeader(c.Writer.Header(), res.Trailer)
		return
	}

	for key, values := range res.Trailer {
		prefixedKey := http.TrailerPrefix + key
		for _, value := range values {
			c.Writer.Header().Add(prefixedKey, value)
		}
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
			if allow := c.engine.allowedMethodsForPath(routeLookupPath(c.Request)); len(allow) > 0 {
				c.Writer.Header().Set("Allow", strings.Join(allow, ", "))
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
	if p.config.ModifyResponse == nil {
		return true
	}
	if err := p.config.ModifyResponse(res); err != nil {
		res.Body.Close()
		p.handleError(c, err)
		return false
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
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, context.Canceled) {
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
	if strings.HasPrefix(raw, "HTTP/") {
		return strings.TrimPrefix(raw, "HTTP/")
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
		for _, token := range strings.Split(connectionValue, ",") {
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
		for _, part := range strings.Split(value, ",") {
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
