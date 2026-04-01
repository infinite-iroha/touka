# 反向代理

Touka 内置了反向代理能力，可以直接把某一组请求转发到后端服务，同时保留 Touka 的路由、中间件与统一错误处理风格。

`touka.ReverseProxy` 返回一个 `HandlerFunc`，因此它可以像普通路由处理器一样直接挂到 `GET`、`ANY`、路由组等位置。

## 最简单的用法

```go
package main

import (
    "log"
    "net/url"

    "github.com/infinite-iroha/touka"
)

func main() {
    r := touka.Default()

    target, err := url.Parse("http://127.0.0.1:9000")
    if err != nil {
        log.Fatal(err)
    }

    r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
        Target: target,
    }))

    _ = r.Run(":8080")
}
```

当客户端访问 `http://127.0.0.1:8080/api/users` 时，请求会被转发到 `http://127.0.0.1:9000/api/users`。

## 带基础路径的代理

如果目标服务部署在一个子路径下，可以直接把目标地址写成带路径的 URL：

```go
target, _ := url.Parse("http://127.0.0.1:9000/backend")

r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
}))
```

此时：

- `/api/users` 会转发到 `/backend/api/users`
- `/api/orders?id=10` 会转发到 `/backend/api/orders?id=10`

目标 URL 自身携带的查询参数也会被保留并与原请求查询参数合并。
合并后的出站查询串会再经过一次规范化处理，因此某些非标准分隔符（例如 `;`）或非法参数片段可能被重编码、折叠或直接丢弃。
这是为了尽量让代理链各跳对查询参数的解析结果保持一致，并减少参数走私这类解析歧义风险。

## 配置项说明

```go
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
```

### `Target`

必填。表示后端目标地址，至少需要提供 `scheme` 和 `host`。

```go
target, _ := url.Parse("http://backend:9000")
```

### `Transport`

可选。用于自定义底层转发所使用的 `http.RoundTripper`。

如果留空，则默认使用 `http.DefaultTransport`。

```go
proxyTransport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 20,
}

r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target:    target,
    Transport: proxyTransport,
}))
```

### `FlushInterval`

控制代理在复制响应体时的主动刷新间隔：

- `0`：不额外定时刷新
- `> 0`：按指定间隔刷新
- `< 0`：每次写入后立即刷新

对于 SSE 和无 `Content-Length` 的流式响应，Touka 会自动立即刷新，不依赖该配置。

### `BufferPool`

可选。用于为响应体复制过程提供可复用的字节缓冲区，以减少大响应或高并发代理场景下的临时内存分配。

如果留空，Touka 会在复制响应体时按需分配默认缓冲区。

```go
type bytePool struct {
    pool sync.Pool
}

func (p *bytePool) Get() []byte {
    if buf, ok := p.pool.Get().([]byte); ok {
        return buf
    }
    return make([]byte, 32*1024)
}

func (p *bytePool) Put(buf []byte) {
    if cap(buf) >= 32*1024 {
        p.pool.Put(buf[:32*1024])
    }
}

proxyPool := &bytePool{}

r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target:     target,
    BufferPool: proxyPool,
}))
```

通常只有在您已经观察到明显的分配压力，或代理的响应体较大、吞吐较高时，才需要专门配置它。

### `ModifyRequest`

在请求真正发往后端前，对出站请求做最后修改。

常见用途：

- 覆盖 `Host`
- 增加鉴权头
- 重写路径
- 注入内部追踪头

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
    ModifyRequest: func(req *http.Request) {
        req.Header.Set("X-Internal-Token", "gateway-token")
    },
}))
```

### `ModifyResponse`

在后端返回响应后、写回客户端前，对响应做额外处理。

注意：`ModifyResponse` 也会作用于 `101 Switching Protocols` 响应。
如果该代理路由需要转发 WebSocket 或其他 Upgrade 流量，请不要在这里消费、完全缓冲，或替换 `resp.Body` 为只读对象；后续升级流程仍然要求它保留 `io.ReadWriteCloser` 能力。
更稳妥的做法是对 `101` 响应直接跳过这类处理。

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
    ModifyResponse: func(resp *http.Response) error {
        if resp.StatusCode == http.StatusSwitchingProtocols {
            return nil
        }
        resp.Header.Set("X-Proxy", "touka")
        return nil
    },
}))
```

如果该函数返回错误，会转入 `ErrorHandler` 或默认的 `502 Bad Gateway` 处理流程。

### `ErrorHandler`

用于处理连接后端失败、协议升级失败、`ModifyResponse` 返回错误等情况。

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
    ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
        w.WriteHeader(http.StatusBadGateway)
        _, _ = w.Write([]byte("upstream unavailable"))
    },
}))
```

### `PreserveHost`

默认情况下，代理请求的 `Host` 会跟随后端目标地址。

如果设置为 `true`，则会保留客户端原始 `Host`。

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target:       target,
    PreserveHost: true,
}))
```

这在某些依赖原始域名进行路由或租户识别的后端服务中会比较有用。

## 转发头策略

Touka 支持两类常见的代理转发头：

- 兼容性更好的 `X-Forwarded-*`
- 标准化的 `Forwarded`（RFC 7239）

可选值：

```go
const (
    ForwardedBoth ForwardedHeadersPolicy = iota
    ForwardedNone
    ForwardedXForwardedOnly
    ForwardedRFC7239Only
)
```

推荐默认使用 `ForwardedBoth`。

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target:           target,
    ForwardedHeaders: touka.ForwardedBoth,
    ForwardedBy:      "_gateway-1",
    Via:              "edge-1",
}))
```

如果您配置了 `ForwardedBy`，它必须是一个符合 RFC 7239 的 node identifier。

- IPv4：`203.0.113.43`
- IPv6 / 带端口：`[2001:db8::17]:443`
- 匿名标识：`_gateway-1`
- 未知：`unknown`

像 `gateway-1` 这类普通 token 不再被视为合法的 `by=` 值。

`Via` 不是“留空即禁用”的开关。当前实现中：

- 如果 `Via` 非空，则使用该值追加 `Via`
- 如果 `Via` 为空，则会回退到固定值 `touka-engine`

因此，把 `Via` 留空时，发送出去的请求仍会包含 `Via` 头，只是使用默认标识 `touka-engine`。

如果您希望上游清楚区分不同入口、环境或网关实例，仍然建议显式设置一个稳定且可公开暴露的代理标识，例如：

```go
r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
    Via:    "edge-gateway",
}))
```

当前版本没有提供“完全禁用追加 Via”的单独配置项，因此不要把空字符串当作关闭手段。

### Touka 会如何处理这些头？

Touka 会尽量遵循代理链语义：

- 已有的 `X-Forwarded-For` 会保留，并在末尾追加当前 hop 的客户端 IP
- 已有的 `Forwarded` 会保留，并在末尾追加当前 hop 的条目
- 已有的 `X-Forwarded-Host` 与 `X-Forwarded-Proto` 会优先保留；如果缺失，则由当前请求补齐
- `Via` 会追加当前代理标识

这意味着在 Touka 前面还有一层可信代理（如 Nginx、Traefik、Cloudflare、网关）时，上游服务仍然可以看到完整的代理链。

如果您**不信任**客户端传入的这些头，请在进入 `ReverseProxy` 之前自行清理，或在 `ModifyRequest` 中显式重写。

## 协议升级与流式响应

Touka 的反向代理实现支持以下能力：

- `CONNECT` 隧道转发（HTTP/1.x）
- `Connection: Upgrade` / `Upgrade` 协议升级转发
- WebSocket 等 101 Switching Protocols 场景
- SSE（Server-Sent Events）立即刷新
- Trailer 透传
- 1xx 响应透传
- `TRACE` / `OPTIONS` 上的 `Max-Forwards` 递减与本地终止处理

例如，代理 WebSocket 服务：

```go
target, _ := url.Parse("http://127.0.0.1:9001")

r.ANY("/ws/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
    Target: target,
}))
```

## Hop-by-hop 头处理

根据 HTTP 代理语义，Touka 在转发时会移除连接级别的 hop-by-hop 头，避免把只应作用于单跳连接的头继续传给下游。

典型包括：

- `Connection`
- `Proxy-Connection`
- `Keep-Alive`
- `Proxy-Authenticate`
- `Proxy-Authorization`
- `TE`
- `Trailer`
- `Transfer-Encoding`
- `Upgrade`

同时，若请求本身是合法的协议升级请求，Touka 会在剥离后重新补回必要的 `Connection: Upgrade` 与 `Upgrade` 头。

## 一个更完整的例子

```go
package main

import (
    "log"
    "net/http"
    "net/url"
    "time"

    "github.com/infinite-iroha/touka"
)

func main() {
    r := touka.Default()

    target, err := url.Parse("http://127.0.0.1:9000")
    if err != nil {
        log.Fatal(err)
    }

    r.ANY("/api/*path", touka.ReverseProxy(touka.ReverseProxyConfig{
        Target:           target,
        ForwardedHeaders: touka.ForwardedBoth,
        ForwardedBy:      "_gateway-1",
        Via:              "gateway-1",
        FlushInterval:    100 * time.Millisecond,
        ModifyRequest: func(req *http.Request) {
            req.Header.Set("X-Gateway", "touka")
        },
        ModifyResponse: func(resp *http.Response) error {
            resp.Header.Set("X-Proxy", "touka")
            return nil
        },
        ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
            w.WriteHeader(http.StatusBadGateway)
            _, _ = w.Write([]byte("bad gateway"))
        },
    }))

    if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
        log.Fatal(err)
    }
}
```

## 与 `SetForwardByClientIP` 的关系

`ReverseProxy` 负责把请求转发给后端，并维护代理链头。

而 `SetForwardByClientIP` / `SetRemoteIPHeaders` 是 Touka 在**接收请求**时，用于解析当前请求客户端 IP 的逻辑。

两者通常会一起出现，但解决的是两个不同方向的问题：

- `ReverseProxy`：出站转发
- `SetForwardByClientIP`：入站解析

如果您的 Touka 本身就部署在其他代理之后，建议同时正确配置这两部分。
