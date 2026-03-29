// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2025 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Event 代表一个服务器发送事件(SSE).
type Event struct {
	// Event 是事件的名称.
	Event string
	// Data 是事件的内容, 可以是多行文本.
	Data string
	// Id 是事件的唯一标识符.
	Id string
	// Retry 是指定客户端在连接丢失后应等待多少毫秒后尝试重新连接.
	Retry string
}

// Render 将事件格式化并写入给定的 writer.
// 通过逐行处理数据, 此方法可防止因数据中包含换行符而导致的CRLF注入问题.
// 为了性能, 它使用 bytes.Buffer 并通过 WriteTo 直接写入, 以避免不必要的内存分配.
func (e *Event) Render(w io.Writer) error {
	var buf bytes.Buffer

	if len(e.Id) > 0 {
		buf.WriteString("id: ")
		buf.WriteString(e.Id)
		buf.WriteString("\n")
	}
	if len(e.Event) > 0 {
		buf.WriteString("event: ")
		buf.WriteString(e.Event)
		buf.WriteString("\n")
	}
	if len(e.Data) > 0 {
		lines := strings.SplitSeq(e.Data, "\n")
		for line := range lines {
			buf.WriteString("data: ")
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}
	if len(e.Retry) > 0 {
		buf.WriteString("retry: ")
		buf.WriteString(e.Retry)
		buf.WriteString("\n")
	}

	// 每个事件都以一个额外的换行符结尾.
	buf.WriteString("\n")

	// 直接将 buffer 的内容写入 writer, 避免生成中间字符串.
	_, err := buf.WriteTo(w)
	return err
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
//	            default:
//	                eventChan <- touka.Event{
//	                    Id:   fmt.Sprintf("%d", i),
//	                    Data: "hello from channel",
//	                }
//	                time.Sleep(2 * time.Second)
//	            }
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
	fl, _ := c.Writer.(http.Flusher)
	reqCtx := c.Request.Context()

	goroutineExited := make(chan struct{})

	// 写入 goroutine: 从 eventChan 消费事件并写入响应.
	go func() {
		defer close(goroutineExited)

		for {
			select {
			case event, ok := <-eventChan:
				if !ok {
					return
				}
				if err := event.Render(c.Writer); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			case <-reqCtx.Done():
				return
			}
		}
	}()

	// 阻塞直到:
	// 1. 写入 goroutine 退出 (eventChan 关闭或写入失败)
	// 2. 客户端断开连接 (reqCtx 取消)
	select {
	case <-goroutineExited:
	case <-reqCtx.Done():
	}
}
