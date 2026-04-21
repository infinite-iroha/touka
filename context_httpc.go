// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"

	"github.com/WJQSERVER-STUDIO/httpc"
)

// contextHTTPClient 包装 httpc.Client，自动关联请求的 Context
// 当请求被取消时，出站 HTTP 请求也会自动取消
type contextHTTPClient struct {
	client *httpc.Client
	ctx    context.Context
}

// NewRequestBuilder 创建请求构建器，自动关联请求 Context
func (c *contextHTTPClient) NewRequestBuilder(method, urlStr string) *httpc.RequestBuilder {
	return c.client.NewRequestBuilder(method, urlStr).WithContext(c.ctx)
}

// GET 创建 GET 请求构建器
func (c *contextHTTPClient) GET(urlStr string) *httpc.RequestBuilder {
	return c.client.GET(urlStr).WithContext(c.ctx)
}

// POST 创建 POST 请求构建器
func (c *contextHTTPClient) POST(urlStr string) *httpc.RequestBuilder {
	return c.client.POST(urlStr).WithContext(c.ctx)
}

// PUT 创建 PUT 请求构建器
func (c *contextHTTPClient) PUT(urlStr string) *httpc.RequestBuilder {
	return c.client.PUT(urlStr).WithContext(c.ctx)
}

// DELETE 创建 DELETE 请求构建器
func (c *contextHTTPClient) DELETE(urlStr string) *httpc.RequestBuilder {
	return c.client.DELETE(urlStr).WithContext(c.ctx)
}

// PATCH 创建 PATCH 请求构建器
func (c *contextHTTPClient) PATCH(urlStr string) *httpc.RequestBuilder {
	return c.client.PATCH(urlStr).WithContext(c.ctx)
}

// HEAD 创建 HEAD 请求构建器
func (c *contextHTTPClient) HEAD(urlStr string) *httpc.RequestBuilder {
	return c.client.HEAD(urlStr).WithContext(c.ctx)
}

// OPTIONS 创建 OPTIONS 请求构建器
func (c *contextHTTPClient) OPTIONS(urlStr string) *httpc.RequestBuilder {
	return c.client.OPTIONS(urlStr).WithContext(c.ctx)
}
