// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"net/http"
)

const (
	defaultMemory = 32 << 20 // 32 MB, Gin 的默认值，用于 ParseMultipartForm
)

type H map[string]any // map简写, 类似gin.H

type Handle func(http.ResponseWriter, *http.Request, Params)

// HandlerFunc 定义框架处理函数的类型，包括中间件和最终的路由处理函数。
type HandlerFunc func(*Context)

// HandlersChain 定义处理函数链（中间件栈）的类型。
type HandlersChain []HandlerFunc

// IRouter 定义了路由注册的接口，提供路由分组和HTTP方法注册的能力。
type IRouter interface {
	Group(relativePath string, handlers ...HandlerFunc) IRouter // 创建路由分组
	Use(middleware ...HandlerFunc) IRouter                      // 应用中间件到当前组或子组

	Handle(httpMethod, relativePath string, handlers ...HandlerFunc) // 注册通用HTTP方法
	GET(relativePath string, handlers ...HandlerFunc)
	POST(relativePath string, handlers ...HandlerFunc)
	PUT(relativePath string, handlers ...HandlerFunc)
	DELETE(relativePath string, handlers ...HandlerFunc)
	PATCH(relativePath string, handlers ...HandlerFunc)
	HEAD(relativePath string, handlers ...HandlerFunc)
	OPTIONS(relativePath string, handlers ...HandlerFunc)
	ANY(relativePath string, handlers ...HandlerFunc) // 注册所有HTTP方法
}

// RouteInfo 包含一个已注册路由的详细信息。
// 由 Router.GetRouters() 方法返回。
type RouteInfo struct {
	Method  string // HTTP 方法 (GET, POST, PUT, DELETE 等)
	Path    string // 路由路径
	Handler string // 处理函数名称
	Group   string // 路由分组
}

// 维护一个Methods列表
var (
	MethodGet     = "GET"
	MethodHead    = "HEAD"
	MethodPost    = "POST"
	MethodPut     = "PUT"
	MethodPatch   = "PATCH"
	MethodDelete  = "DELETE"
	MethodConnect = "CONNECT"
	MethodOptions = "OPTIONS"
	MethodTrace   = "TRACE"
)

var MethodsSet = map[string]struct{}{
	MethodGet:     {},
	MethodHead:    {},
	MethodPost:    {},
	MethodPut:     {},
	MethodPatch:   {},
	MethodDelete:  {},
	MethodConnect: {},
	MethodOptions: {},
	MethodTrace:   {},
}
