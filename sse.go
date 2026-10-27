// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2025 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event 代表一个服务器发送事件(SSE).
type Event struct {
	// Event 是事件的名称.
	// CR/LF 会被移除, 因为 SSE 的字段值不允许包含行终止符.
	Event string
	// Data 是事件的内容, 可以是多行文本.
	// 行分隔符可以是 LF, CRLF 或 CR, 渲染时会被规范化为逐行的 data 字段.
	// 为空时不会发送 data 字段, 此时客户端不会派发该事件(Id 仍会更新 Last-Event-ID).
	Data string
	// Id 是事件的唯一标识符.
	// CR/LF/NUL 会被移除, 因为 SSE 的字段值不允许包含这些字符.
	Id string
	// Retry 是指定客户端在连接丢失后应等待多少毫秒后尝试重新连接.
	// 必须是十进制 ASCII 数字, 否则该字段不会被发送(客户端会忽略非法的值).
	Retry string
}

// Render 将事件格式化并写入给定的 writer.
// 输出遵循 WHATWG HTML "Server-sent events" 定义的事件流格式:
//   - 字段格式为 "name: value", 冒号后的一个前导空格会被客户端剥离;
//   - 数据按行拆分为多个 data 字段, 行分隔符支持 LF/CRLF/CR;
//   - 事件以空行结束, 客户端收到空行后才会派发该事件;
//   - 字段值中不会出现 CR/LF, 避免破坏事件帧结构或让客户端解析出额外的字段.
//
// 为了性能, 它使用 bytes.Buffer 并通过 WriteTo 直接写入, 以避免不必要的内存分配.
func (e *Event) Render(w io.Writer) error {
	var buf bytes.Buffer

	if id := sanitizeSSEId(e.Id); len(id) > 0 {
		buf.WriteString("id: ")
		buf.WriteString(id)
		buf.WriteString("\n")
	}
	if name := sanitizeSSEValue(e.Event); len(name) > 0 {
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteString("\n")
	}
	if len(e.Data) > 0 {
		lines := strings.SplitSeq(normalizeSSELineEndings(e.Data), "\n")
		for line := range lines {
			buf.WriteString("data: ")
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	// retry 字段只接受十进制 ASCII 数字, 其它值会被客户端忽略, 因此不再输出.
	if isASCIIDigits(e.Retry) {
		buf.WriteString("retry: ")
		buf.WriteString(e.Retry)
		buf.WriteString("\n")
	}

	// 每个事件都以一个额外的换行符结尾, 客户端收到空行后才会派发该事件.
	buf.WriteString("\n")

	_, err := buf.WriteTo(w)
	return err
}

// sseLineEndingNormalizer 将 CRLF 与 CR 统一规范化为 LF.
var sseLineEndingNormalizer = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// normalizeSSELineEndings 把数据中的 CRLF/CR 统一转换为 LF.
// SSE 的行终止符可以是 LF/CRLF/CR, 但字段值内部不允许出现 CR,
// 否则客户端会把裸 CR 当作行结束符, 截断数据甚至解析出额外的字段.
func normalizeSSELineEndings(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return sseLineEndingNormalizer.Replace(s)
}

// sanitizeSSEValue 移除字段值中的 CR/LF.
// SSE 的 ABNF 规定字段值只允许出现除 CR/LF 以外的字符.
func sanitizeSSEValue(s string) string {
	return stripSSEBytes(s, "\r\n")
}

// sanitizeSSEId 在 sanitizeSSEValue 的基础上额外移除 NUL.
// 客户端会忽略包含 NUL 的 id 字段, 移除后可以保留 id 的实际含义.
func sanitizeSSEId(s string) string {
	return stripSSEBytes(s, "\r\n\x00")
}

// stripSSEBytes 移除字符串中的指定 ASCII 字节, 无需移除时原样返回.
// UTF-8 的多字节序列不会包含这些 ASCII 字节, 因此按字节处理是安全的.
func stripSSEBytes(s, drop string) string {
	idx := strings.IndexAny(s, drop)
	if idx < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:idx])
	for i := idx; i < len(s); i++ {
		if !strings.ContainsRune(drop, rune(s[i])) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// isASCIIDigits 判断字符串是否非空且只由 ASCII 数字组成.
// SSE 规范要求 retry 字段的值必须是十进制 ASCII 数字, 否则客户端会忽略该字段.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// EventStream 启动一个 SSE 事件流.
// 这是推荐的、更简单安全的方式, 采用阻塞和回调的设计, 框架负责管理连接生命周期.
//
// 详细用法:
//
//	r.GET("/sse/callback", func(c *touka.Context) {
//	    // streamer 回调函数会在一个循环中被调用.
//	    c.EventStream(func(w io.Writer) bool {
//	        event := touka.Event{
//	            Event: "time-tick",
//	            Data:  time.Now().Format(time.RFC1123),
//	        }
//
//	        if err := event.Render(w); err != nil {
//	            // 发生写入错误, 停止发送.
//	            return false // 返回 false 结束事件流.
//	        }
//
//	        time.Sleep(2 * time.Second)
//	        return true // 返回 true 继续事件流.
//	    })
//	    // 当事件流结束后(例如客户端关闭页面), 这行代码会被执行.
//	    fmt.Println("Client disconnected from /sse/callback")
//	})
func (c *Context) EventStream(streamer func(w io.Writer) bool) {
	// 为现代网络协议优化头部.
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Del("Connection")
	c.Writer.Header().Del("Transfer-Encoding")

	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush() // 直接调用, ResponseWriter 接口保证了 Flush 方法的存在.

	for {
		select {
		case <-c.Request.Context().Done():
			return
		default:
			if !streamer(c.Writer) {
				return
			}
			c.Writer.Flush()
		}
	}
}

// EventStreamChan 返回用于 SSE 事件流的 channel.
// 这是为高级并发场景设计的、更灵活的API.
//
// 与 EventStream 回调模式类似, 此方法是阻塞的: handler 会在此方法中停留,
// 直到事件 channel 被关闭 (close eventChan) 或客户端断开连接.
// 这保证了 Context 不会在 SSE 流期间被 pool 回收.
// 事件由 handler goroutine 直接写入, 因此本方法返回后不会再有 goroutine 操作该响应.
// 客户端断开(或服务端关闭)时, 会通过写超时打断可能阻塞中的写入.
//
// eventChan 必须在调用此方法之前创建, 以便调用者可以在独立的 goroutine 中发送事件.
// 调用者必须在完成后 close(eventChan) 来结束流.
// 生产者 goroutine 必须在 select 中监听 c.Request.Context().Done(), 否则在客户端断开时会产生 goroutine 泄漏.
//
// 详细用法:
//
//	r.GET("/sse/channel", func(c *touka.Context) {
//	    eventChan := make(chan touka.Event)
//
//	    // 在独立的 goroutine 中异步发送事件.
//	    go func() {
//	        defer close(eventChan) // 完成后关闭 channel 以结束事件流.
//
//	        for i := 1; i <= 5; i++ {
//	            select {
//	            case <-c.Request.Context().Done():
//	                return // 客户端已断开, 退出 goroutine.
//	            case eventChan <- touka.Event{
//	                Id:   fmt.Sprintf("%d", i),
//	                Data: "hello from channel",
//	            }:
//	            }
//	            time.Sleep(2 * time.Second)
//	        }
//	    }()
//
//	    // 阻塞直到事件流结束.
//	    c.EventStreamChan(eventChan)
//	})
func (c *Context) EventStreamChan(eventChan <-chan Event) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Del("Connection")
	c.Writer.Header().Del("Transfer-Encoding")

	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	// 捕获稳定的引用, 不持有 *Context 指针, 以免 Context 被 pool 回收后出现竞态.
	w := c.Writer
	fl, _ := w.(http.Flusher)
	reqCtx := c.Request.Context()

	// 看门狗: 客户端断开(或服务端关闭)时打断可能阻塞中的写入.
	// 它只设置写超时, 不写入响应内容, 因此与写入操作并发安全.
	watchdogStop := make(chan struct{})
	watchdogExited := make(chan struct{})
	go func() {
		defer close(watchdogExited)
		select {
		case <-reqCtx.Done():
		case <-watchdogStop:
			return
		}
		if err := abortSSEWrite(w); err != nil {
			// 底层 writer 不支持写超时时, 阻塞中的写入无法被中断,
			// 只能等待写入自行结束(例如客户端断开导致写入报错).
			if errors.Is(err, http.ErrNotSupported) {
				c.Warnf("SSE: 底层 ResponseWriter 不支持写超时, 阻塞中的写入无法被中断: %v", err)
			} else {
				c.Errorf("SSE: 设置写超时失败: %v", err)
			}
		}
	}()

	// handler 返回前必须等待看门狗退出, 否则它可能在响应被回收后继续访问 ResponseWriter.
	// 看门狗只做一次设置写超时的调用, 因此这里的等待不会阻塞.
	defer func() {
		close(watchdogStop)
		<-watchdogExited
	}()

	// 事件在 handler goroutine 内写入: 写入结束后本方法才返回,
	// 因此不会与 net/http 的响应收尾流程(或 Context 对象池复用)争抢 ResponseWriter.
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				return
			}
			if err := event.Render(w); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		case <-reqCtx.Done():
			return
		}
	}
}

// abortSSEWrite 通过设置一个已经过去的写超时来打断可能阻塞中的响应写入.
// 写超时直接作用于底层连接, 不会与写入操作产生数据竞争.
// 返回的错误表示底层 writer 不支持写超时, 此时阻塞中的写入无法被中断.
func abortSSEWrite(w ResponseWriter) error {
	return http.NewResponseController(w).SetWriteDeadline(time.Now())
}
