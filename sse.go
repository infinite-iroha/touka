// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"bytes"
	"fmt"
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
		lines := strings.Split(e.Data, "\n")
		for _, line := range lines {
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
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.Errorf("streaming unsupported: http.ResponseWriter does not implement http.Flusher")
		return
	}

	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		default:
			if !streamer(c.Writer) {
				return
			}
			flusher.Flush()
		}
	}
}

// EventStreamChan 返回用于 SSE 事件流的 channel.
// 这是为高级并发场景设计的、更灵活的API.
// 调用者必须负责关闭 event channel 并处理 error channel 以避免 goroutine 泄漏.
//
// 详细用法:
//
//	r.GET("/sse/channel", func(c *touka.Context) {
//	    eventChan, errChan := c.EventStreamChan()
//
//	    // 必须在独立的goroutine中处理错误和连接断开.
//	    go func() {
//	        if err := <-errChan; err != nil {
//	            c.Errorf("SSE channel error: %v", err)
//	        }
//	    }()
//
//	    // 在另一个goroutine中异步发送事件.
//	    go func() {
//	        // 重要: 必须在逻辑结束时关闭channel, 以通知框架.
//	        defer close(eventChan)
//
//	        for i := 1; i <= 5; i++ {
//	            event := touka.Event{
//	                Id:   fmt.Sprintf("%d", i),
//	                Data: "hello from channel",
//	            }
//	            eventChan <- event
//	            time.Sleep(2 * time.Second)
//	        }
//	    }()
//	})
func (c *Context) EventStreamChan() (chan<- Event, <-chan error) {
	eventChan := make(chan Event)
	errChan := make(chan error, 1)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		err := fmt.Errorf("streaming unsupported: http.ResponseWriter does not implement http.Flusher")
		c.Errorf(err.Error())
		errChan <- err
		close(errChan)
		close(eventChan)
		return eventChan, errChan
	}

	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	go func() {
		defer close(errChan)

		for {
			select {
			case event, ok := <-eventChan:
				if !ok {
					return
				}
				if err := event.Render(c.Writer); err != nil {
					errChan <- err
					return
				}
				flusher.Flush()
			case <-c.Request.Context().Done():
				errChan <- c.Request.Context().Err()
				return
			}
		}
	}()

	return eventChan, errChan
}
