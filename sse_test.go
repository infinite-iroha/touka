package touka

import (
	"context"
	"net/http/httptest"
	"strings"
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
