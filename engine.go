package touka

import (
	"context"
	"log"
	"reflect"
	"runtime"
	"strings"

	"net/http"
	"path"

	"sync"

	"github.com/WJQSERVER-STUDIO/httpc"
)

// Last 返回链中的最后一个处理函数。
// 如果链为空，则返回 nil。
func (c HandlersChain) Last() HandlerFunc {
	if len(c) > 0 {
		return c[len(c)-1]
	}
	return nil
}

// Engine 是 Touka 框架的核心，负责路由注册、中间件管理和请求分发。
// 它实现了 http.Handler 接口，可以直接用于 http.ListenAndServe。
type Engine struct {
	methodTrees methodTrees // 存储所有HTTP方法的路由树

	pool sync.Pool // Context Pool 用于复用 Context 对象，提高性能。

	globalHandlers HandlersChain // 全局中间件，应用于所有路由。

	maxParams uint16 // 记录所有路由中最大的参数数量，用于优化 Params 切片的分配。

	// 可配置项，用于控制框架行为，参考 Gin
	RedirectTrailingSlash  bool     // 是否自动重定向带尾部斜杠的路径到不带尾部斜杠的路径 (e.g. /foo/ -> /foo)
	RedirectFixedPath      bool     // 是否自动修复路径中的大小写错误 (e.g. /Foo -> /foo)
	HandleMethodNotAllowed bool     // 是否启用 MethodNotAllowed 处理器
	ForwardByClientIP      bool     // 是否信任 X-Forwarded-For 等头部获取客户端 IP
	RemoteIPHeaders        []string // 用于获取客户端 IP 的头部列表，例如 {"X-Forwarded-For", "X-Real-IP"}
	// TrustedProxies        []string // 可信代理 IP 列表，用于判断是否使用 X-Forwarded-For 等头部 (预留接口)

	HTTPClient *httpc.Client // 用于在此上下文中执行出站 HTTP 请求。

	HTMLRender interface{} // 用于 HTML 模板渲染，可以设置为 *template.Template 或自定义渲染器接口

	routesInfo []RouteInfo // 存储所有注册的路由信息

	errorHandle ErrorHandle // 错误处理

	noRoute HandlerFunc

	unMatchFS         UnMatchFS    // 未匹配下的处理
	unMatchFileServer http.Handler // 处理handle

	serverProtocols     *http.Protocols //服务协议
	Protocols           ProtocolsConfig //协议版本配置
	useDefaultProtocols bool            //是否使用默认协议
}

type ErrorHandle struct {
	useDefault bool
	handler    ErrorHandler
}

type ErrorHandler func(c *Context, code int)

// defaultErrorHandle 默认错误处理
func defaultErrorHandle(c *Context, code int) { // 检查客户端是否已断开连接
	select {
	case <-c.Request.Context().Done():

		return
	default:
		if c.Writer.Written() {
			return
		}
		// 输出json 状态码与状态码对应描述
		c.JSON(code, H{
			"code":    code,
			"message": http.StatusText(code),
		})
		c.Writer.Flush()
		c.Abort()
		return
	}
}

// 默认errorhandle包装 避免竞争意外问题, 保证稳定性
func defaultErrorWarp(handler ErrorHandler) ErrorHandler {
	return func(c *Context, code int) {
		select {
		case <-c.Request.Context().Done():
			return
		default:
			if c.Writer.Written() {
				log.Printf("errpage: response already started for status %d, skipping error page rendering", code)
				return
			}
		}
		handler(c, code)
	}
}

type UnMatchFS struct {
	FSForUnmatched     http.FileSystem
	ServeUnmatchedAsFS bool
}

// ProtocolsConfig 协议版本配置结构体
type ProtocolsConfig struct {
	Http1           bool // 是否启用 HTTP/1.1
	Http2           bool // 是否启用 HTTP/2
	Http2_Cleartext bool // 是否启用 H2C
}

// New 创建并返回一个 Engine 实例。
func New() *Engine {
	engine := &Engine{
		methodTrees:            make(methodTrees, 0, 9), // 常见的HTTP方法有9个 (GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS, CONNECT, TRACE)
		RedirectTrailingSlash:  true,
		RedirectFixedPath:      true,
		HandleMethodNotAllowed: true,
		ForwardByClientIP:      true,
		HTTPClient:             httpc.New(),          // 提供一个默认的 HTTPClient
		routesInfo:             make([]RouteInfo, 0), // 初始化路由信息切片
		globalHandlers:         make(HandlersChain, 0),
		RemoteIPHeaders:        []string{"X-Forwarded-For", "X-Real-IP"},
		errorHandle: ErrorHandle{
			useDefault: true,
			handler:    defaultErrorHandle,
		},
		unMatchFS: UnMatchFS{
			ServeUnmatchedAsFS: false,
		},
	}
	//engine.SetProtocols(GetDefaultProtocolsConfig())
	engine.SetDefaultProtocols()
	// 初始化 Context Pool，为每个新 Context 实例提供一个构造函数
	engine.pool.New = func() interface{} {
		return &Context{
			Writer:     newResponseWriter(nil),            // 初始时可以传入nil，在ServeHTTP中会重新设置实际的 http.ResponseWriter
			Params:     make(Params, 0, engine.maxParams), // 预分配 Params 切片以减少内存分配
			Keys:       make(map[string]interface{}),
			Errors:     make([]error, 0),
			ctx:        context.Background(), // 初始上下文，后续会被请求的 Context 覆盖
			HTTPClient: engine.HTTPClient,
			engine:     engine, // Context 持有 Engine 引用，方便访问 Engine 的配置
		}
	}

	return engine
}

// 生成一个携带默认中间件的Engine
func Default() *Engine {
	engine := New()
	engine.Use(Recovery())
	return engine
}

// === 外部操作方法 ===

// 设置自定义错误处理
func (engine *Engine) SetErrorHandler(handler ErrorHandler) {
	engine.errorHandle.useDefault = false
	engine.errorHandle.handler = defaultErrorWarp(handler)
}

// 获取一个默认错误处理handle
func (engine *Engine) GetDefaultErrHandler() ErrorHandler {
	return defaultErrorHandle
}

// 传入并配置unMatchFS
func (engine *Engine) SetUnMatchFS(fs http.FileSystem) {
	if fs != nil {
		engine.unMatchFS.FSForUnmatched = fs
		engine.unMatchFS.ServeUnmatchedAsFS = true
		engine.unMatchFileServer = http.FileServer(fs)
	} else {
		engine.unMatchFS.ServeUnmatchedAsFS = false
		engine.unMatchFileServer = nil
	}
}

// 获取默认Protocol配置
func GetDefaultProtocolsConfig() *ProtocolsConfig {
	return &ProtocolsConfig{
		Http1:           true,
		Http2:           false,
		Http2_Cleartext: false,
	}
}

// 设置默认Protocols
func (engine *Engine) SetDefaultProtocols() {
	engine.useDefaultProtocols = true
	engine.SetProtocols(GetDefaultProtocolsConfig())
}

// 设置Protocol
func (engine *Engine) SetProtocols(config *ProtocolsConfig) {
	engine.Protocols = *config
	engine.serverProtocols = &http.Protocols{} // 初始化指针
	func() {
		var p http.Protocols
		p.SetHTTP1(config.Http1)
		p.SetHTTP2(config.Http2)
		p.SetUnencryptedHTTP2(config.Http2_Cleartext)
		*engine.serverProtocols = p // 将值赋给指针指向的结构体
	}()
	engine.useDefaultProtocols = false
}

// 配置Req IP来源 Headers
func (engine *Engine) SetRemoteIPHeaders(headers []string) {
	engine.RemoteIPHeaders = headers
}

// SetForwardByClientIP 设置是否信任 X-Forwarded-For 等头部获取客户端 IP。
func (engine *Engine) SetForwardByClientIP(enable bool) {
	engine.ForwardByClientIP = enable
}

// SetHTTPClient 设置 Engine 使用的 httpc.Client。
func (engine *Engine) SetHTTPClient(client *httpc.Client) {
	if client != nil {
		engine.HTTPClient = client
	}
}

// registerMethodTree 内部方法，用于获取或注册对应 HTTP 方法的路由树根节点。
// 如果该方法没有对应的树，则创建一个新的树。
func (engine *Engine) registerMethodTree(method string) *node {
	for _, tree := range engine.methodTrees {
		if tree.method == method {
			return tree.root
		}
	}
	// 如果没有找到，则创建一个新的方法树并添加到列表中
	root := &node{
		nType:    root, // 根节点类型
		fullPath: "/",  // 根路径
	}
	engine.methodTrees = append(engine.methodTrees, methodTree{method: method, root: root})
	return root
}

// addRoute 将一个路由及处理函数链添加到路由树中。
// 这是框架内部路由注册的核心逻辑。
// groupPath 用于记录路由所属的分组路径。
func (engine *Engine) addRoute(method, absolutePath, groupPath string, handlers HandlersChain) { // relativePath 更名为 absolutePath
	if absolutePath == "" {
		panic("absolute path must not be empty")
	}
	if len(handlers) == 0 {
		panic("handlers must not be empty")
	}

	// 检查并更新 maxParams，使用 absolutePath
	if n := countParams(absolutePath); n > engine.maxParams {
		engine.maxParams = n
	}

	root := engine.registerMethodTree(method)
	root.addRoute(absolutePath, handlers) // 调用 node 的 addRoute 方法将路由添加到树中

	handlerName := "unknown"
	if len(handlers) > 0 {
		handlerName = getHandlerName(handlers.Last())
	}

	engine.routesInfo = append(engine.routesInfo, RouteInfo{
		Method:  method,
		Path:    absolutePath, // 使用完整的绝对路径
		Handler: handlerName,
		Group:   groupPath,
	})
}

// getHandlerName 辅助函数，用于获取 HandlerFunc 的名称。
// 注意：这只是一个简单的反射实现，对于匿名函数或闭包，可能返回不可读的名称。
func getHandlerName(h HandlerFunc) string {
	//return reflect.TypeOf(h).Name() // 对于具名函数，返回函数名。对于匿名函数，可能返回空字符串或类似 func123 这样的名称。
	// 更精确的获取函数名需要 import "runtime"
	// pc := reflect.ValueOf(h).Pointer()
	// f := runtime.FuncForPC(pc)
	// return f.Name()

	if h == nil {
		return "nil_handler"
	}
	pc := reflect.ValueOf(h).Pointer()
	f := runtime.FuncForPC(pc)
	return f.Name() // 返回例如 "main.HomeHandler" 或 "touka.Logger"

}

// ServeHTTP 实现了 http.Handler 接口，是 Engine 处理所有 HTTP 请求的入口。
// 每个传入的 HTTP 请求都会调用此方法。
func (engine *Engine) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 从 Context Pool 中获取一个 Context 对象进行复用
	c := engine.pool.Get().(*Context)
	c.reset(w, req) // 重置 Context 对象的状态以适应当前请求

	// 执行请求处理
	engine.handleRequest(c)

	// 将 Context 对象放回 Context Pool，以供下次复用
	engine.pool.Put(c)
}

// handleRequest 负责根据请求查找路由并执行相应的处理函数链。
// 这是路由查找和执行的核心逻辑。
func (engine *Engine) handleRequest(c *Context) {
	httpMethod := c.Request.Method
	requestPath := c.Request.URL.Path

	// 查找对应的路由树的根节点
	rootNode := engine.methodTrees.get(httpMethod) // 这里获取到的 rootNode 已经是 *node 类型
	if rootNode != nil {
		// 查找匹配的节点和处理函数
		// 这里传递 &c.Params 而不是重新创建，以利用 Context 中预分配的容量
		// skippedNodes 内部使用，因此无需从外部传入已分配的 slice
		var skippedNodes []skippedNode // 用于回溯的跳过节点
		// 直接在 rootNode 上调用 getValue 方法
		value := rootNode.getValue(requestPath, &c.Params, &skippedNodes, true) // unescape=true 对路径参数进行 URL 解码

		if value.handlers != nil {
			//c.handlers = engine.combineHandlers(engine.globalHandlers, value.handlers) // 组合全局中间件和路由处理函数
			c.handlers = value.handlers
			c.Next()         // 执行处理函数链
			c.Writer.Flush() // 确保所有缓冲的响应数据被发送
			return
		}

		// 如果没有找到处理函数，检查是否需要重定向（尾部斜杠或大小写修复）
		if httpMethod != http.MethodConnect && requestPath != "/" { // CONNECT 方法和根路径不进行重定向
			if value.tsr && engine.RedirectTrailingSlash {
				// 尾部斜杠重定向：/foo/ -> /foo 或 /foo -> /foo/
				redirectPath := requestPath
				if len(requestPath) > 0 && requestPath[len(requestPath)-1] == '/' {
					redirectPath = requestPath[:len(requestPath)-1]
				} else {
					redirectPath = requestPath + "/"
				}
				c.Redirect(http.StatusMovedPermanently, redirectPath) // 301 永久重定向
				return
			}
			// 尝试不区分大小写的查找
			// 直接在 rootNode 上调用 findCaseInsensitivePath 方法
			ciPath, found := rootNode.findCaseInsensitivePath(requestPath, engine.RedirectTrailingSlash)
			if found && engine.RedirectFixedPath {
				c.Redirect(http.StatusMovedPermanently, BytesToString(ciPath)) // 301 永久重定向到修正后的路径
				return
			}
		}
	}
	/*
		// 如果没有找到路由，且启用了 MethodNotAllowed 处理
		if engine.HandleMethodNotAllowed {
			// 是否是OPTIONS方式
			if httpMethod == http.MethodOptions {
				// 如果是 OPTIONS 请求，尝试查找所有允许的方法
				allowedMethods := []string{}
				for _, treeIter := range engine.methodTrees {
					var tempSkippedNodes []skippedNode
					// 注意这里 treeIter.root 才是正确的，因为 treeIter 是 methodTree 类型
					value := treeIter.root.getValue(requestPath, nil, &tempSkippedNodes, false)
					if value.handlers != nil {
						allowedMethods = append(allowedMethods, treeIter.method)
					}
				}
				if len(allowedMethods) > 0 {
					// 如果找到了允许的方法，返回 200 OK 并设置 Allow 头部
					c.Writer.Header().Set("Allow", strings.Join(allowedMethods, ", "))
					c.Status(http.StatusOK)
					return
				}
			}
			// 尝试遍历所有方法树，看是否有其他方法可以匹配当前路径
			for _, treeIter := range engine.methodTrees {
				if treeIter.method == httpMethod { // 已经处理过当前方法，跳过
					continue
				}
				var tempSkippedNodes []skippedNode // 用于临时查找，不影响主 Context
				// 注意这里 treeIter.root 才是正确的，因为 treeIter 是 methodTree 类型
				value := treeIter.root.getValue(requestPath, nil, &tempSkippedNodes, false) // 只查找是否存在，不需要参数
				if value.handlers != nil {
					// 使用定义的ErrorHandle处理
					engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
					return
				}
			}
		}

		// 是否开启了UnMatchFS
		if engine.unMatchFS.ServeUnmatchedAsFS {
			// 若不是GET HEAD OPTIONS则返回405
			if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
				// 使用 http.FileServer 处理未匹配的请求
				fileServer := http.FileServer(engine.unMatchFS.FSForUnmatched)
				//ecw := newErrorCapturingResponseWriter(c, c.engine.errorHandle.handler)
				ecw := AcquireErrorCapturingResponseWriter(c, c.engine.errorHandle.handler)
				defer ReleaseErrorCapturingResponseWriter(ecw)
				fileServer.ServeHTTP(ecw, c.Request)
				ecw.processAfterFileServer()
				return
			} else {
				log.Printf("Not Allowed Method: %s", c.Request.Method)
				// 若为OPTIONS
				if c.Request.Method == http.MethodOptions {
					//返回allow get
					c.Writer.Header().Set("Allow", "GET")
					c.Status(http.StatusOK)
					c.Abort()
					return
				} else {
					engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
					return
				}
			}

		} else {
			engine.errorHandle.handler(c, http.StatusNotFound)
			return
		}
	*/

	// 构建处理链
	// 组合全局中间件和路由处理函数
	handlers := engine.globalHandlers

	// 如果启用了 MethodNotAllowed 处理，并且没有找到精确匹配的路由
	// 则在全局中间件之后添加 MethodNotAllowed 处理器
	if engine.HandleMethodNotAllowed {
		handlers = append(handlers, MethodNotAllowed())
	}

	// 如果启用了 UnMatchFS 处理，并且没有找到精确匹配的路由和 MethodNotAllowed
	// 则在处理链的最后添加 UnMatchFS 处理器
	if engine.unMatchFS.ServeUnmatchedAsFS {
		handlers = append(handlers, unMatchFSHandle())
	}

	// 如果用户设置了 NoRoute 处理器，且没有匹配到任何路由、MethodNotAllowed 或 UnMatchFS
	// 则在处理链的最后添加 NoRoute 处理器
	if engine.noRoute != nil {
		handlers = append(handlers, engine.noRoute)
	}

	handlers = append(handlers, NotFound())

	c.handlers = handlers
	c.Next()         // 执行处理函数链
	c.Writer.Flush() // 确保所有缓冲的响应数据被发送

}

// UnMatchFS HandleFunc
func unMatchFSHandle() HandlerFunc {
	return func(c *Context) {
		engine := c.engine
		// 确保 engine.unMatchFileServer 存在
		if !engine.unMatchFS.ServeUnmatchedAsFS || engine.unMatchFileServer == nil {
			c.Next() // 如果未配置或 FileSystem 为 nil，则继续处理链
			return
		}
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			// 使用 http.FileServer 处理未匹配的请求
			//fileServer := http.FileServer(engine.unMatchFS.FSForUnmatched)
			//ecw := newErrorCapturingResponseWriter(c, c.engine.errorHandle.handler)
			ecw := AcquireErrorCapturingResponseWriter(c)
			defer ReleaseErrorCapturingResponseWriter(ecw)
			c.engine.unMatchFileServer.ServeHTTP(ecw, c.Request)
			ecw.processAfterFileServer()
			c.Abort()
			return
		} else {
			if engine.noRoute == nil {
				// 若为OPTIONS
				if c.Request.Method == http.MethodOptions {
					//返回allow get
					c.Writer.Header().Set("Allow", "GET")
					c.Status(http.StatusOK)
					c.Abort()
					return
				} else {
					engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
					return
				}
			} else {
				c.Next()
			}
		}
	}
}

// 405中间件
func MethodNotAllowed() HandlerFunc {
	return func(c *Context) {
		httpMethod := c.Request.Method
		requestPath := c.Request.URL.Path
		engine := c.engine
		// 是否是OPTIONS方式
		if httpMethod == http.MethodOptions {
			// 如果是 OPTIONS 请求，尝试查找所有允许的方法
			allowedMethods := []string{}
			for _, treeIter := range engine.methodTrees {
				var tempSkippedNodes []skippedNode
				// 注意这里 treeIter.root 才是正确的，因为 treeIter 是 methodTree 类型
				value := treeIter.root.getValue(requestPath, nil, &tempSkippedNodes, false)
				if value.handlers != nil {
					allowedMethods = append(allowedMethods, treeIter.method)
				}
			}
			if len(allowedMethods) > 0 {
				// 如果找到了允许的方法，返回 200 OK 并设置 Allow 头部
				c.Writer.Header().Set("Allow", strings.Join(allowedMethods, ", "))
				c.Status(http.StatusOK)
				return
			}
		}
		// 尝试遍历所有方法树，看是否有其他方法可以匹配当前路径
		for _, treeIter := range engine.methodTrees {
			if treeIter.method == httpMethod { // 已经处理过当前方法，跳过
				continue
			}
			var tempSkippedNodes []skippedNode // 用于临时查找，不影响主 Context
			// 注意这里 treeIter.root 才是正确的，因为 treeIter 是 methodTree 类型
			value := treeIter.root.getValue(requestPath, nil, &tempSkippedNodes, false) // 只查找是否存在，不需要参数
			if value.handlers != nil {
				// 使用定义的ErrorHandle处理
				engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
				return
			}
		}
	}
}

// 404最后处理
func NotFound() HandlerFunc {
	return func(c *Context) {
		engine := c.engine
		engine.errorHandle.handler(c, http.StatusNotFound)
		return
	}
}

// 传入并设置NoRoute (这不是最后一个处理, 你仍可以next到默认的404处理)
func (Engine *Engine) NoRoute(handler HandlerFunc) {
	Engine.noRoute = handler
}

// combineHandlers 组合多个处理函数链为一个。
// 这是构建完整处理链（全局中间件 + 组中间件 + 路由处理函数）的关键。
func (engine *Engine) combineHandlers(h1 HandlersChain, h2 HandlersChain) HandlersChain {
	finalSize := len(h1) + len(h2)
	mergedHandlers := make(HandlersChain, finalSize)
	copy(mergedHandlers, h1)
	copy(mergedHandlers[len(h1):], h2)
	return mergedHandlers
}

// Use 将全局中间件添加到 Engine。
// 这些中间件将应用于所有注册的路由。
func (engine *Engine) Use(middleware ...HandlerFunc) IRouter {
	engine.globalHandlers = append(engine.globalHandlers, middleware...)
	return engine
}

// Handle 注册通用 HTTP 方法的路由。
// 这是所有具体 HTTP 方法注册的基础方法。
func (engine *Engine) Handle(httpMethod, relativePath string, handlers ...HandlerFunc) {
	absolutePath := path.Join("/", relativePath) // 修正：统一使用 path.Join 进行路径拼接
	// 修正：将全局中间件与此路由的处理函数合并
	fullHandlers := engine.combineHandlers(engine.globalHandlers, handlers)
	engine.addRoute(httpMethod, absolutePath, "/", fullHandlers)
}

// GET 注册 GET 方法的路由。
func (engine *Engine) GET(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodGet, relativePath, handlers...)
}

// POST 注册 POST 方法的路由。
func (engine *Engine) POST(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodPost, relativePath, handlers...)
}

// PUT 注册 PUT 方法的路由。
func (engine *Engine) PUT(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodPut, relativePath, handlers...)
}

// DELETE 注册 DELETE 方法的路由。
func (engine *Engine) DELETE(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodDelete, relativePath, handlers...)
}

// PATCH 注册 PATCH 方法的路由。
func (engine *Engine) PATCH(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodPatch, relativePath, handlers...)
}

// HEAD 注册 HEAD 方法的路由。
func (engine *Engine) HEAD(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodHead, relativePath, handlers...)
}

// OPTIONS 注册 OPTIONS 方法的路由。
func (engine *Engine) OPTIONS(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodOptions, relativePath, handlers...)
}

// ANY 注册所有常见 HTTP 方法的路由。
func (engine *Engine) ANY(relativePath string, handlers ...HandlerFunc) {
	engine.Handle(http.MethodGet, relativePath, handlers...)
	engine.Handle(http.MethodPost, relativePath, handlers...)
	engine.Handle(http.MethodPut, relativePath, handlers...)
	engine.Handle(http.MethodDelete, relativePath, handlers...)
	engine.Handle(http.MethodPatch, relativePath, handlers...)
	engine.Handle(http.MethodHead, relativePath, handlers...)
	engine.Handle(http.MethodOptions, relativePath, handlers...)
}

// GetRouterInfo 返回所有已注册的路由信息。
func (engine *Engine) GetRouterInfo() []RouteInfo {
	return engine.routesInfo
}

// Group 创建一个新的路由组。
// 路由组允许将具有相同前缀路径和/或共享中间件的路由组织在一起。
func (engine *Engine) Group(relativePath string, handlers ...HandlerFunc) IRouter {
	return &RouterGroup{
		Handlers: engine.combineHandlers(engine.globalHandlers, handlers), // 继承全局中间件
		basePath: path.Join("/", relativePath),
		engine:   engine, // 指向 Engine 实例
	}
}

// RouterGroup 表示一个路由分组，可以添加组特定的中间件和路由。
// 它也实现了 IRouter 接口，允许嵌套分组。
type RouterGroup struct {
	Handlers HandlersChain // 组中间件，仅应用于当前组及其子组的路由
	basePath string        // 组路径前缀
	engine   *Engine       // 指向 Engine 实例，用于注册路由到全局路由树
}

// Use 将中间件应用于当前路由组。
// 这些中间件将应用于当前组及其子组的所有路由。
func (group *RouterGroup) Use(middleware ...HandlerFunc) IRouter {
	group.Handlers = append(group.Handlers, middleware...)
	return group
}

// Handle 注册通用 HTTP 方法的路由到当前组。
// 路径是相对于当前组的 basePath。
func (group *RouterGroup) Handle(httpMethod, relativePath string, handlers ...HandlerFunc) {
	absolutePath := path.Join(group.basePath, relativePath)
	fullHandlers := group.engine.combineHandlers(group.Handlers, handlers)
	group.engine.addRoute(httpMethod, absolutePath, group.basePath, fullHandlers)
}

// GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS, ANY 方法与 Engine 类似，只是通过 Group 的 Handle 方法注册。
func (group *RouterGroup) GET(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodGet, relativePath, handlers...)
}
func (group *RouterGroup) POST(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodPost, relativePath, handlers...)
}
func (group *RouterGroup) PUT(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodPut, relativePath, handlers...)
}
func (group *RouterGroup) DELETE(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodDelete, relativePath, handlers...)
}
func (group *RouterGroup) PATCH(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodPatch, relativePath, handlers...)
}
func (group *RouterGroup) HEAD(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodHead, relativePath, handlers...)
}
func (group *RouterGroup) OPTIONS(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodOptions, relativePath, handlers...)
}
func (group *RouterGroup) ANY(relativePath string, handlers ...HandlerFunc) {
	group.Handle(http.MethodGet, relativePath, handlers...)
	group.Handle(http.MethodPost, relativePath, handlers...)
	group.Handle(http.MethodPut, relativePath, handlers...)
	group.Handle(http.MethodDelete, relativePath, handlers...)
	group.Handle(http.MethodPatch, relativePath, handlers...)
	group.Handle(http.MethodHead, relativePath, handlers...)
	group.Handle(http.MethodOptions, relativePath, handlers...)
}

// Group 为当前组创建一个新的子组。
func (group *RouterGroup) Group(relativePath string, handlers ...HandlerFunc) IRouter {
	return &RouterGroup{
		Handlers: group.engine.combineHandlers(group.Handlers, handlers),
		basePath: path.Join(group.basePath, relativePath),
		engine:   group.engine, // 指向 Engine 实例
	}
}

// == 其他操作方式 ===

// Static FileServer 传入一个文件夹路径, 使用FileServer进行处理
func (engine *Engine) Static(relativePath, rootPath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	rootPath = path.Clean(rootPath)

	// 确保相对路径以 '/' 结尾，以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	// 创建一个文件系统处理器
	fileServer := http.FileServer(http.Dir(rootPath))

	// 注册一个捕获所有路径的路由，使用自定义处理器
	// 注意：这里使用 ANY 方法，但 FileServer 通常只处理 GET 和 HEAD
	// 我们可以通过在处理函数内部检查方法来限制
	engine.ANY(relativePath+"*filepath", func(c *Context) {
		// 检查是否是 GET 或 HEAD 方法
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			// 如果不是，且启用了 MethodNotAllowed 处理，则继续到 MethodNotAllowed 中间件
			if engine.HandleMethodNotAllowed {
				c.Next()
			} else {
				// 否则，返回 405 Method Not Allowed
				engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
			}
			return
		}

		requestPath := c.Request.URL.Path

		// 获取捕获到的文件路径参数
		filepath := c.Param("filepath")

		// 构造文件服务器需要处理的请求路径
		// FileServer 会将请求路径与 http.Dir 的根路径结合
		// 我们需要移除相对路径前缀，只保留文件路径部分
		// 例如，如果 relativePath 是 "/static/"，请求是 "/static/js/app.js"
		// FileServer 需要的路径是 "/js/app.js"
		// 这里的 filepath 参数已经包含了 "/" 前缀，例如 "/js/app.js"
		// 所以直接使用 filepath 即可
		c.Request.URL.Path = filepath

		// 使用自定义的 ResponseWriter 包装器来捕获 FileServer 可能返回的错误状态码
		// 这样我们可以在 FileServer 返回 404 或 403 时，使用 Engine 的 ErrorHandler 进行统一处理
		ecw := AcquireErrorCapturingResponseWriter(c)
		defer ReleaseErrorCapturingResponseWriter(ecw)

		//
		// 调用 FileServer 处理请求
		fileServer.ServeHTTP(ecw, c.Request)

		// 在 FileServer 处理完成后，检查是否捕获到错误状态码，并调用 ErrorHandler
		ecw.processAfterFileServer()

		// 恢复原始请求路径，以便后续中间件或日志记录使用
		c.Request.URL.Path = requestPath

		// 中止处理链，因为 FileServer 已经处理了响应
		c.Abort()
	})
}

// Group的Static
func (group *RouterGroup) Static(relativePath, rootPath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	rootPath = path.Clean(rootPath)

	// 确保相对路径以 '/' 结尾，以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	// 创建一个文件系统处理器
	fileServer := http.FileServer(http.Dir(rootPath))

	// 注册一个捕获所有路径的路由，使用自定义处理器
	// 注意：这里使用 ANY 方法，但 FileServer 通常只处理 GET 和 HEAD
	// 我们可以通过在处理函数内部检查方法来限制
	group.ANY(relativePath+"*filepath", func(c *Context) {
		// 检查是否是 GET 或 HEAD 方法
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			// 如果不是，且启用了 MethodNotAllowed 处理，则继续到 MethodNotAllowed 中间件
			if group.engine.HandleMethodNotAllowed {
				c.Next()
			} else {
				// 否则，返回 405 Method Not Allowed
				group.engine.errorHandle.handler(c, http.StatusMethodNotAllowed)
			}
			return
		}

		requestPath := c.Request.URL.Path

		// 获取捕获到的文件路径参数
		filepath := c.Param("filepath")

		// 构造文件服务器需要处理的请求路径
		// FileServer 会将请求路径与 http.Dir 的根路径结合
		// 我们需要移除相对路径前缀，只保留文件路径部分
		// 例如，如果 relativePath 是 "/static/"，请求是 "/static/js/app.js"
		// FileServer 需要的路径是 "/js/app.js"
		// 这里的 filepath 参数已经包含了 "/" 前缀，例如 "/js/app.js"
		// 所以直接使用 filepath 即可
		c.Request.URL.Path = filepath

		// 使用自定义的 ResponseWriter 包装器来捕获 FileServer 可能返回的错误状态码
		// 这样我们可以在 FileServer 返回 404 或 403 时，使用 Engine 的 ErrorHandler 进行统一处理
		ecw := AcquireErrorCapturingResponseWriter(c)
		defer ReleaseErrorCapturingResponseWriter(ecw)

		//
		// 调用 FileServer 处理请求
		fileServer.ServeHTTP(ecw, c.Request)

		// 在 FileServer 处理完成后，检查是否捕获到错误状态码，并调用 ErrorHandler
		ecw.processAfterFileServer()

		// 恢复原始请求路径，以便后续中间件或日志记录使用
		c.Request.URL.Path = requestPath

		// 中止处理链，因为 FileServer 已经处理了响应
		c.Abort()
	})
}
