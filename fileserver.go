// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"errors"
	"net/http"
	"path"
	"strings"
)

// === FileServer相关 ===

var allowedFileServerMethods = map[string]struct{}{
	http.MethodGet:  {},
	http.MethodHead: {},
}

var (
	ErrInputFSisNil     = errors.New("input FS is nil")
	ErrMethodNotAllowed = errors.New("method not allowed")
)

// FileServer方式, 返回一个HandleFunc, 统一化处理
func FileServer(fs http.FileSystem) HandlerFunc {
	if fs == nil {
		return func(c *Context) {
			c.ErrorUseHandle(http.StatusInternalServerError, ErrInputFSisNil)
		}
	}

	fileServerInstance := http.FileServer(fs)
	return func(c *Context) {
		FileServerHandleServe(c, fileServerInstance)

		// 中止处理链,因为 FileServer 已经处理了响应
		c.Abort()
	}
}

func FileServerHandleServe(c *Context, fsHandle http.Handler) {
	if fsHandle == nil {
		c.AddError(ErrInputFSisNil)
		// 500
		c.ErrorUseHandle(http.StatusInternalServerError, ErrInputFSisNil)
		return
	}

	// 检查是否是 GET 或 HEAD 方法
	if _, ok := allowedFileServerMethods[c.Request.Method]; !ok {
		// 如果不是,且启用了 MethodNotAllowed 处理,则继续到 MethodNotAllowed 中间件
		if c.engine.HandleMethodNotAllowed {
			c.Next()
		} else {
			if c.engine.noRoute == nil {
				if c.Request.Method == http.MethodOptions {
					//返回allow get
					c.Writer.Header().Set("Allow", "GET, HEAD")
					c.Status(http.StatusOK)
					c.Abort()
					return
				} else {
					// 否则,返回 405 Method Not Allowed
					c.engine.errorHandle.handler(c, http.StatusMethodNotAllowed, ErrMethodNotAllowed)
				}
			} else {
				c.Next()
			}
		}
		return
	}

	// 使用自定义的 ResponseWriter 包装器来捕获 FileServer 可能返回的错误状态码
	ecw := AcquireErrorCapturingResponseWriter(c)
	defer ReleaseErrorCapturingResponseWriter(ecw)

	// 调用 http.FileServer 处理请求
	fsHandle.ServeHTTP(ecw, c.Request)

	// 在 FileServer 处理完成后,检查是否捕获到错误状态码,并调用 ErrorHandler
	ecw.processAfterFileServer()
}

// StaticDir 传入一个文件夹路径, 使用FileServer进行处理
// r.StaticDir("/test/*filepath", "/var/www/test")
func (engine *Engine) StaticDir(relativePath, rootPath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	rootPath = path.Clean(rootPath)

	// 确保相对路径以 '/' 结尾,以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	// 创建一个文件系统处理器
	fileServer := http.FileServer(http.Dir(rootPath))

	// 注册一个捕获所有路径的路由,使用自定义处理器
	// 注意：这里使用 ANY 方法,但 FileServer 通常只处理 GET 和 HEAD
	// 我们可以通过在处理函数内部检查方法来限制
	engine.ANY(relativePath+"*filepath", GetStaticDirHandleFunc(fileServer))
}

// Group的StaticDir方式
func (group *RouterGroup) StaticDir(relativePath, rootPath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	rootPath = path.Clean(rootPath)

	// 确保相对路径以 '/' 结尾,以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	// 创建一个文件系统处理器
	fileServer := http.FileServer(http.Dir(rootPath))

	// 注册一个捕获所有路径的路由,使用自定义处理器
	// 注意：这里使用 ANY 方法,但 FileServer 通常只处理 GET 和 HEAD
	// 我们可以通过在处理函数内部检查方法来限制
	group.ANY(relativePath+"*filepath", GetStaticDirHandleFunc(fileServer))
}

// GetStaticDirHandleFunc
func (engine *Engine) GetStaticDirHandle(rootPath string) HandlerFunc {
	// 清理路径
	rootPath = path.Clean(rootPath)

	// 创建一个文件系统处理器
	fileServer := http.FileServer(http.Dir(rootPath))

	return GetStaticDirHandleFunc(fileServer)
}

// GetStaticDirHandleFunc
func (group *RouterGroup) GetStaticDirHandle(rootPath string) HandlerFunc { // 清理路径
	return group.engine.GetStaticDirHandle(rootPath)
}

// GetStaticDirHandle
func GetStaticDirHandleFunc(fsHandle http.Handler) HandlerFunc {
	return func(c *Context) {
		requestPath := c.Request.URL.Path

		// 获取捕获到的文件路径参数
		filepath := c.Param("filepath")

		// 构造文件服务器需要处理的请求路径
		c.Request.URL.Path = filepath

		FileServerHandleServe(c, fsHandle)

		// 恢复原始请求路径,以便后续中间件或日志记录使用
		c.Request.URL.Path = requestPath

		// 中止处理链,因为 FileServer 已经处理了响应
		c.Abort()
	}
}

// Static File 传入一个文件路径, 使用FileServer进行处理
func (engine *Engine) StaticFile(relativePath, filePath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	filePath = path.Clean(filePath)

	FileHandle := engine.GetStaticFileHandle(filePath)

	// 注册一个精确匹配的路由
	engine.GET(relativePath, FileHandle)
	engine.HEAD(relativePath, FileHandle)
	engine.OPTIONS(relativePath, FileHandle)

}

// Group的StaticFile
func (group *RouterGroup) StaticFile(relativePath, filePath string) {
	// 清理路径
	relativePath = path.Clean(relativePath)
	filePath = path.Clean(filePath)

	FileHandle := group.GetStaticFileHandle(filePath)

	// 注册一个精确匹配的路由
	group.GET(relativePath, FileHandle)
	group.HEAD(relativePath, FileHandle)
	group.OPTIONS(relativePath, FileHandle)
}

// GetStaticFileHandleFunc
func (engine *Engine) GetStaticFileHandle(filePath string) HandlerFunc {
	// 清理路径
	filePath = path.Clean(filePath)

	// 创建一个文件系统处理器,指向包含目标文件的目录
	dir := path.Dir(filePath)
	fileName := path.Base(filePath)
	fileServer := http.FileServer(http.Dir(dir))

	return GetStaticFileHandleFunc(fileServer, fileName)
}

// GetStaticFileHandleFunc
func (group *RouterGroup) GetStaticFileHandle(filePath string) HandlerFunc {
	// 清理路径
	filePath = path.Clean(filePath)

	// 创建一个文件系统处理器,指向包含目标文件的目录
	dir := path.Dir(filePath)
	fileName := path.Base(filePath)
	fileServer := http.FileServer(http.Dir(dir))

	return GetStaticFileHandleFunc(fileServer, fileName)
}

// GetStaticFileHandleFunc
func GetStaticFileHandleFunc(fsHandle http.Handler, fileName string) HandlerFunc {
	return func(c *Context) {
		requestPath := c.Request.URL.Path

		// 构造文件服务器需要处理的请求路径
		c.Request.URL.Path = "/" + fileName

		FileServerHandleServe(c, fsHandle)

		// 恢复原始请求路径
		c.Request.URL.Path = requestPath

		// 中止处理链,因为 FileServer 已经处理了响应
		c.Abort()
	}
}

// StaticFS
func (engine *Engine) StaticFS(relativePath string, fs http.FileSystem) {
	// 清理路径
	relativePath = path.Clean(relativePath)

	// 确保相对路径以 '/' 结尾,以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	fileServer := http.StripPrefix(relativePath, http.FileServer(fs))
	engine.ANY(relativePath+"*filepath", GetStaticFSHandleFunc(fileServer))
}

// Group的StaticFS
func (group *RouterGroup) StaticFS(relativePath string, fs http.FileSystem) {
	// 清理路径
	relativePath = path.Clean(relativePath)

	// 确保相对路径以 '/' 结尾,以便 FileServer 正确处理子路径
	if !strings.HasSuffix(relativePath, "/") {
		relativePath += "/"
	}

	fileServer := http.StripPrefix(relativePath, http.FileServer(fs))
	group.ANY(relativePath+"*filepath", GetStaticFSHandleFunc(fileServer))
}

// GetStaticFSHandleFunc
func GetStaticFSHandleFunc(fsHandle http.Handler) HandlerFunc {
	return func(c *Context) {

		FileServerHandleServe(c, fsHandle)

		// 中止处理链,因为 FileServer 已经处理了响应
		c.Abort()
	}
}

// GetStaticFSHandleFunc
func (engine *Engine) GetStaticFSHandle(fs http.FileSystem) HandlerFunc {
	fileServer := http.FileServer(fs)
	return GetStaticFSHandleFunc(fileServer)
}

// GetStaticFSHandleFunc
func (group *RouterGroup) GetStaticFSHandle(fs http.FileSystem) HandlerFunc {
	fileServer := http.FileServer(fs)
	return GetStaticFSHandleFunc(fileServer)
}
