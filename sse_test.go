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
		// 必须在启动 goroutine 前取出请求上下文, 否则 goroutine 会在 handler
		// 返回后访问已被对象池复用的 Context.
		ctx := c.Request.Context()
		eventChan := make(chan Event)
		go func() {
			defer close(eventChan)
			payload := strings.Repeat("sse-payload-", 512)
			for {
				select {
				case <-ctx.Done():
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

// sseParsedEvent 是按 WHATWG HTML "Interpreting an event stream" 解析出的事件.
type sseParsedEvent struct {
	data        string
	eventType   string
	lastEventID string
}

// sseParser 按 WHATWG HTML 的规则解析事件流, 用于验证渲染结果是否符合规范.
type sseParser struct {
	data        strings.Builder
	eventType   string
	lastEventID string
	retry       string
	events      []sseParsedEvent
}

func parseSSEStream(stream string) *sseParser {
	p := &sseParser{}
	for _, line := range splitSSELines(stream) {
		switch {
		case line == "":
			p.dispatch()
		case strings.HasPrefix(line, ":"):
			// 注释行, 忽略.
		case strings.Contains(line, ":"):
			name, value, _ := strings.Cut(line, ":")
			p.processField(name, strings.TrimPrefix(value, " "))
		default:
			p.processField(line, "")
		}
	}
	return p
}

func (p *sseParser) processField(name, value string) {
	switch name {
	case "event":
		p.eventType = value
	case "data":
		p.data.WriteString(value)
		p.data.WriteByte('\n')
	case "id":
		if !strings.ContainsRune(value, 0) {
			p.lastEventID = value
		}
	case "retry":
		if value != "" && allASCIIDigits(value) {
			p.retry = value
		}
	}
}

func (p *sseParser) dispatch() {
	if p.data.Len() == 0 {
		p.data.Reset()
		p.eventType = ""
		return
	}
	data := strings.TrimSuffix(p.data.String(), "\n")
	eventType := p.eventType
	if eventType == "" {
		// 用户代理在事件类型为空时使用默认类型 "message".
		eventType = "message"
	}
	p.events = append(p.events, sseParsedEvent{
		data:        data,
		eventType:   eventType,
		lastEventID: p.lastEventID,
	})
	p.data.Reset()
	p.eventType = ""
}

// allASCIIDigits 判断字符串是否非空且只由 ASCII 数字组成.
// 独立于被测代码实现, 避免测试与实现互相印证.
func allASCIIDigits(s string) bool {
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

// splitSSELines 按 SSE 的行结束规则(CRLF/CR/LF)切分文本.
func splitSSELines(s string) []string {
	var lines []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			lines = append(lines, cur.String())
			cur.Reset()
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case '\n':
			lines = append(lines, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return lines
}

// renderEvent 渲染事件并返回原始字节.
func renderEvent(t *testing.T, e *Event) string {
	t.Helper()
	var sb strings.Builder
	if err := e.Render(&sb); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return sb.String()
}

// TestEventRenderConformsToSSEFormat verifies that the rendered event follows
// the event stream format of the WHATWG HTML standard and is interpreted as
// expected by a spec conformant client.
func TestEventRenderConformsToSSEFormat(t *testing.T) {
	tests := []struct {
		name        string
		event       Event
		wantData    string
		wantType    string
		wantLastID  string
		wantNoEvent bool
	}{
		{
			name:       "simple",
			event:      Event{Data: "hello"},
			wantData:   "hello",
			wantType:   "message",
			wantLastID: "",
		},
		{
			name:     "multi line data",
			event:    Event{Data: "hello\nworld"},
			wantData: "hello\nworld",
			wantType: "message",
		},
		{
			name:       "named event with id",
			event:      Event{Event: "tick", Id: "42", Data: "payload"},
			wantData:   "payload",
			wantType:   "tick",
			wantLastID: "42",
		},
		{
			name:     "leading space is preserved",
			event:    Event{Data: " hello"},
			wantData: " hello",
			wantType: "message",
		},
		{
			name:     "crlf is normalized to lf",
			event:    Event{Data: "hello\r\nworld"},
			wantData: "hello\nworld",
			wantType: "message",
		},
		{
			name:     "lone cr does not break the frame",
			event:    Event{Data: "hello\rworld"},
			wantData: "hello\nworld",
			wantType: "message",
		},
		{
			name:        "empty data is not dispatched",
			event:       Event{Id: "7"},
			wantNoEvent: true,
			wantLastID:  "7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := renderEvent(t, &tt.event)
			if strings.ContainsRune(raw, '\r') {
				t.Fatalf("rendered event contains a bare CR: %q", raw)
			}
			if !strings.HasSuffix(raw, "\n\n") {
				t.Fatalf("event must end with a blank line, got %q", raw)
			}

			p := parseSSEStream(raw)
			if tt.wantNoEvent {
				if len(p.events) != 0 {
					t.Fatalf("expected no dispatched event, got %d", len(p.events))
				}
			} else {
				if len(p.events) != 1 {
					t.Fatalf("expected exactly one event, got %d (%q)", len(p.events), raw)
				}
				got := p.events[0]
				if got.data != tt.wantData {
					t.Errorf("data = %q, want %q", got.data, tt.wantData)
				}
				wantType := tt.wantType
				if wantType == "" {
					wantType = "message"
				}
				if got.eventType != wantType {
					t.Errorf("eventType = %q, want %q", got.eventType, wantType)
				}
				if got.lastEventID != tt.wantLastID {
					t.Errorf("lastEventID = %q, want %q", got.lastEventID, tt.wantLastID)
				}
			}
			if p.lastEventID != tt.wantLastID {
				t.Errorf("last event ID buffer = %q, want %q", p.lastEventID, tt.wantLastID)
			}
		})
	}
}

// TestEventRenderPreventsFieldInjection verifies that CR/LF inside field values
// cannot break out of the event frame and inject additional fields.
func TestEventRenderPreventsFieldInjection(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		wantData string
	}{
		{name: "data with cr", event: Event{Data: "safe\rdata: injected"}, wantData: "safe\ndata: injected"},
		{name: "data with crlf", event: Event{Data: "safe\r\ndata: injected"}, wantData: "safe\ndata: injected"},
		{name: "data with lf", event: Event{Data: "safe\ndata: injected"}, wantData: "safe\ndata: injected"},
		{name: "id with lf", event: Event{Id: "1\ndata: injected", Data: "payload"}, wantData: "payload"},
		{name: "event with lf", event: Event{Event: "tick\ndata: injected", Data: "payload"}, wantData: "payload"},
		{name: "retry with lf", event: Event{Retry: "1000\ndata: injected", Data: "payload"}, wantData: "payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := renderEvent(t, &tt.event)
			if strings.ContainsRune(raw, '\r') {
				t.Fatalf("rendered event contains a bare CR: %q", raw)
			}
			// 除 data 字段外不允许出现额外的字段.
			for _, line := range splitSSELines(raw) {
				if line == "" || strings.HasPrefix(line, "data: ") {
					continue
				}
				name, _, _ := strings.Cut(line, ":")
				switch name {
				case "id", "event", "retry":
				default:
					t.Fatalf("unexpected field %q in %q", name, raw)
				}
			}
			// 注入的内容只能作为普通数据出现, 不会产生额外的事件或字段.
			p := parseSSEStream(raw)
			if len(p.events) != 1 {
				t.Fatalf("field injection produced %d events: %q", len(p.events), raw)
			}
			if p.events[0].data != tt.wantData {
				t.Fatalf("data = %q, want %q (raw %q)", p.events[0].data, tt.wantData, raw)
			}
		})
	}
}

// TestEventRenderRetry verifies the retry field value space defined by the
// specification: only ASCII digits are meaningful, other values are ignored by
// the client and therefore must not be sent.
func TestEventRenderRetry(t *testing.T) {
	raw := renderEvent(t, &Event{Retry: "5000", Data: "x"})
	if !strings.Contains(raw, "retry: 5000\n") {
		t.Fatalf("missing retry field: %q", raw)
	}

	for _, invalid := range []string{"", "5s", "abc", "5 000", "0x10"} {
		raw := renderEvent(t, &Event{Retry: invalid, Data: "x"})
		if strings.Contains(raw, "retry:") {
			t.Fatalf("invalid retry %q must not be sent: %q", invalid, raw)
		}
	}
}
