// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2026 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	_ "unsafe"

	"golang.org/x/net/http2"
)

var enableHTTP2ExtendedConnectOnce sync.Once

//go:linkname xnetDisableHTTP2ExtendedConnectProtocol golang.org/x/net/http2.disableExtendedConnectProtocol
var xnetDisableHTTP2ExtendedConnectProtocol bool

func enableHTTP2ExtendedConnectProtocol() {
	enableHTTP2ExtendedConnectOnce.Do(func() {
		xnetDisableHTTP2ExtendedConnectProtocol = false
	})
}

func configureHTTP2ExtendedConnectServer(srv *http.Server) error {
	if srv == nil {
		return nil
	}
	enableHTTP2ExtendedConnectProtocol()
	return http2.ConfigureServer(srv, nil)
}

func newHTTP2ExtendedConnectTransport(target *url.URL) http.RoundTripper {
	enableHTTP2ExtendedConnectProtocol()

	transport := &http2.Transport{}
	if target == nil || !strings.EqualFold(target.Scheme, "http") {
		return transport
	}

	transport.AllowHTTP = true
	transport.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, addr)
	}
	return transport
}
