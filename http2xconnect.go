// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2026 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
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

func newHTTP2ExtendedConnectTransport() http.RoundTripper {
	enableHTTP2ExtendedConnectProtocol()
	transport := cloneDefaultTransport()
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	transport.Protocols.SetHTTP2(true)
	return transport
}

func newHTTP1BridgeTransport() http.RoundTripper {
	return newHTTP1BridgeTransportWithTLSConfig(&tls.Config{NextProtos: []string{"http/1.1"}})
}

func newHTTP1BridgeTransportWithTLSConfig(tlsConfig *tls.Config) http.RoundTripper {
	transport := cloneDefaultTransport()
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	transport.TLSClientConfig = tlsConfig
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	if len(transport.TLSClientConfig.NextProtos) == 0 {
		transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}
	return transport
}

func newH2CTransport() http.RoundTripper {
	transport := cloneDefaultTransport()
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetUnencryptedHTTP2(true)
	return transport
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
