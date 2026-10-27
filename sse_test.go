package touka

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEventStreamChanBlocksHandler verifies that EventStreamChan blocks until
// the event channel is closed.
func TestEventStreamChanBlocksHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sse", nil)
	c, _ := CreateTestContextWithRequest(rr, req)

	handlerReturned := make(chan struct{})
	eventChan := make(chan Event)

	// Start producer goroutine before EventStreamChan blocks
	go func() {
		defer close(eventChan)
		time.Sleep(30 * time.Millisecond)
		eventChan <- Event{Data: "hello"}
		time.Sleep(30 * time.Millisecond)
	}()

	go func() {
		c.EventStreamChan(eventChan)
		close(handlerReturned)
	}()

	// Wait for goroutine to start
	time.Sleep(10 * time.Millisecond)

	// Handler should NOT have returned (eventChan not closed)
	select {
	case <-handlerReturned:
		t.Fatal("Handler returned before eventChan was closed - EventStreamChan is not blocking")
	case <-time.After(40 * time.Millisecond):
		// good, still blocking
	}

	// Wait for producer to finish (30+30ms + margin)
	select {
	case <-handlerReturned:
		// good, handler returned
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Handler did not return after eventChan was closed")
	}
}

// TestEventStreamChanUnblocksOnClientDisconnect verifies the handler returns
// when the request context is cancelled, even if eventChan is never closed.
func TestEventStreamChanUnblocksOnClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	c, _ := CreateTestContextWithRequest(rr, req)

	eventChan := make(chan Event)
	handlerReturned := make(chan struct{})

	// Producer never closes eventChan
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case eventChan <- Event{Data: "tick"}:
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	go func() {
		c.EventStreamChan(eventChan)
		close(handlerReturned)
	}()

	// Handler should NOT have returned
	select {
	case <-handlerReturned:
		t.Fatal("Handler returned before stream ended")
	case <-time.After(60 * time.Millisecond):
		// good, still blocked
	}

	// Cancel context to simulate client disconnect
	cancel()

	select {
	case <-handlerReturned:
		// good
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Handler did not return after client disconnect")
	}
}

// TestEventStreamChanWritesEvents verifies the SSE event format is correct.
func TestEventStreamChanWritesEvents(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/sse", nil)
	c, _ := CreateTestContextWithRequest(rr, req)

	eventChan := make(chan Event)

	go func() {
		defer close(eventChan)
		eventChan <- Event{Id: "1", Event: "tick", Data: "hello\nworld"}
		eventChan <- Event{Id: "2", Data: "second"}
	}()

	c.EventStreamChan(eventChan)

	body := rr.Body.String()

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %q", ct)
	}

	if !strings.Contains(body, "id: 1") {
		t.Fatal("missing id field in first event")
	}
	if !strings.Contains(body, "event: tick") {
		t.Fatal("missing event field in first event")
	}
	if !strings.Contains(body, "data: hello") {
		t.Fatal("missing data line 1 in first event")
	}
	if !strings.Contains(body, "data: world") {
		t.Fatal("missing data line 2 in first event")
	}
	if !strings.Contains(body, "id: 2") {
		t.Fatal("missing id field in second event")
	}
	if !strings.Contains(body, "data: second") {
		t.Fatal("missing data in second event")
	}
}

// blockingSSEWriter 模拟一个写入被阻塞的客户端(例如 socket 发送缓冲区已满).
type blockingSSEWriter struct {
	header  http.Header
	entered chan struct{} // 首次进入 Write 时关闭
	release chan struct{} // 关闭后允许 Write 返回
	once    sync.Once

	handlerDone       atomic.Bool // EventStreamChan 是否已经返回
	mu                sync.Mutex
	wroteAfterHandler bool
}

func newBlockingSSEWriter() *blockingSSEWriter {
	return &blockingSSEWriter{
		header:  make(http.Header),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingSSEWriter) Header() http.Header { return w.header }

func (w *blockingSSEWriter) WriteHeader(int) {}

func (w *blockingSSEWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	if w.handlerDone.Load() {
		w.mu.Lock()
		w.wroteAfterHandler = true
		w.mu.Unlock()
	}
	return len(p), nil
}

func (w *blockingSSEWriter) Flush() {}

func (w *blockingSSEWriter) markHandlerDone() { w.handlerDone.Store(true) }

func (w *blockingSSEWriter) wroteAfterHandlerDone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wroteAfterHandler
}

// TestEventStreamChanWaitsForWriterGoroutine verifies that EventStreamChan does
// not return while its internal writer goroutine is still writing to the
// response. Returning early races with net/http response finalization, which
// flushes and recycles the response's bufio.Writer.
func TestEventStreamChanWaitsForWriterGoroutine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	writer := newBlockingSSEWriter()
	c, _ := CreateTestContextWithRequest(writer, req)

	eventChan := make(chan Event)
	go func() {
		select {
		case eventChan <- Event{Data: "hello"}:
		case <-ctx.Done():
		}
	}()

	handlerDone := make(chan struct{})
	go func() {
		c.EventStreamChan(eventChan)
		writer.markHandlerDone()
		close(handlerDone)
	}()

	// 等待写入 goroutine 进入 Write 并阻塞.
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("writer goroutine never entered Write")
	}

	// 模拟客户端断开.
	cancel()

	// handler 必须等待写入 goroutine 退出.
	select {
	case <-handlerDone:
		t.Fatal("EventStreamChan returned while the writer goroutine was still writing")
	case <-time.After(100 * time.Millisecond):
	}

	// 解除写入阻塞, 写入 goroutine 应当退出并让 handler 返回.
	close(writer.release)

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("EventStreamChan did not return after the writer goroutine exited")
	}

	if writer.wroteAfterHandlerDone() {
		t.Fatal("response was written after EventStreamChan returned")
	}
}

// TestEventStreamChanDisconnectKeepsOtherResponsesIntact verifies that a client
// disconnecting from an SSE stream does not corrupt other responses: the stale
// writer goroutine must not write into a recycled bufio.Writer nor into the
// next request's pooled Context.
func TestEventStreamChanDisconnectKeepsOtherResponsesIntact(t *testing.T) {
	engine := New()
	engine.GET("/sse", func(c *Context) {
		eventChan := make(chan Event)
		go func() {
			defer close(eventChan)
			payload := strings.Repeat("sse-payload-", 512)
			for {
				select {
				case <-c.Request.Context().Done():
					return
				case eventChan <- Event{Data: payload}:
				}
			}
		}()
		c.EventStreamChan(eventChan)
	})
	engine.GET("/ping", func(c *Context) {
		c.String(http.StatusOK, "pong")
	})

	ts := httptest.NewServer(engine)
	defer ts.Close()

	// 使用裸 TCP 连接, 读取部分响应后直接断开, 制造最恶劣的中断场景.
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	host := ts.Listener.Addr().String()
	if _, err := conn.Write([]byte("GET /sse HTTP/1.1\r\nHost: " + host + "\r\nAccept: text/event-stream\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read response: %v", err)
	}
	conn.Close()

	// 断开之后的普通请求必须完全正常.
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 3; i++ {
		resp, err := client.Get(ts.URL + "/ping")
		if err != nil {
			t.Fatalf("get /ping: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read /ping body: %v", err)
		}
		if string(body) != "pong" {
			t.Fatalf("unexpected /ping body: %q", body)
		}
	}
}
