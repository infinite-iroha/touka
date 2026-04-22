package touka

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestApplyDefaultServerConfig(t *testing.T) {
	engine := New()

	// 1. 测试默认协议
	srv1 := &http.Server{}
	engine.applyDefaultServerConfig(srv1)

	if srv1.Protocols == nil {
		t.Fatal("srv1.Protocols should not be nil after applyDefaultServerConfig")
	}

	// 默认配置是 Http1: true, Http2: false, Http2_Cleartext: false
	if !srv1.Protocols.HTTP1() {
		t.Error("Expected HTTP/1 to be enabled by default")
	}
	if srv1.Protocols.HTTP2() {
		t.Error("Expected HTTP/2 to be disabled by default")
	}

	// 2. 测试自定义协议
	engine.SetProtocols(&ProtocolsConfig{
		Http1:           true,
		Http2:           true,
		Http2_Cleartext: true,
	})

	srv2 := &http.Server{}
	engine.applyDefaultServerConfig(srv2)

	if srv2.Protocols == nil {
		t.Fatal("srv2.Protocols should not be nil after applyDefaultServerConfig")
	}

	if !srv2.Protocols.HTTP1() {
		t.Error("Expected HTTP/1 to be enabled after SetProtocols")
	}
	if !srv2.Protocols.HTTP2() {
		t.Error("Expected HTTP/2 to be enabled after SetProtocols")
	}
	if !srv2.Protocols.UnencryptedHTTP2() {
		t.Error("Expected Unencrypted HTTP/2 to be enabled after SetProtocols")
	}

	// 3. 再次更改协议并验证
	engine.SetProtocols(&ProtocolsConfig{
		Http1:           false,
		Http2:           true,
		Http2_Cleartext: false,
	})

	srv3 := &http.Server{}
	engine.applyDefaultServerConfig(srv3)

	if srv3.Protocols == nil {
		t.Fatal("srv3.Protocols should not be nil")
	}
	if srv3.Protocols.HTTP1() {
		t.Error("Expected HTTP/1 to be disabled")
	}
	if !srv3.Protocols.HTTP2() {
		t.Error("Expected HTTP/2 to be enabled")
	}
}

func TestTLSRunDefaultsProtocolInheritance(t *testing.T) {
	engine := New()

	srv := buildMainServer(engine, runConfig{addr: ":443", mode: runModeHTTPS, tlsConfig: &tls.Config{}})

	if !srv.Protocols.HTTP2() {
		t.Error("TLS run defaults: expected HTTP/2 to be enabled for default config")
	}

	// 模拟用户设置了自定义协议后进入 TLS 运行模式
	engine = New()
	engine.SetProtocols(&ProtocolsConfig{
		Http1: true,
		Http2: false, // 用户明确不想要 HTTP/2
	})

	srv2 := buildMainServer(engine, runConfig{addr: ":443", mode: runModeHTTPS, tlsConfig: &tls.Config{}})

	if srv2.Protocols.HTTP2() {
		t.Error("TLS run defaults: expected HTTP/2 to remain disabled when user set custom protocols")
	}
}
