package touka

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMergeCtx_NoParents(t *testing.T) {
	ctx, cancel := MergeCtx()
	defer cancel()

	if ctx.Err() != nil {
		t.Fatal("expected no error before cancel")
	}
	cancel()
	if ctx.Err() == nil {
		t.Fatal("expected error after cancel")
	}
}

func TestMergeCtx_SingleParent(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())

	ctx, cancel := MergeCtx(parent)
	defer cancel()

	if ctx.Err() != nil {
		t.Fatal("expected no error before parent cancel")
	}

	parentCancel()
	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after parent cancel")
	}
}

func TestMergeCtx_MultipleParents_FirstCancels(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	cancel1()
	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after p1 cancel")
	}
	// p2 should still be fine
	if p2.Err() != nil {
		t.Fatal("expected p2 to be unaffected")
	}
}

func TestMergeCtx_MultipleParents_SecondCancels(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	cancel2()
	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after p2 cancel")
	}
}

func TestMergeCtx_ExternalCancel(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	ctx, cancel := MergeCtx(p1, p2)

	cancel()
	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after external cancel")
	}
}

func TestMergeCtx_CausePropagation(t *testing.T) {
	testErr := errors.New("test cause")

	p1, cancel1 := context.WithCancelCause(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	cancel1(testErr)
	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after p1 cancel")
	}

	cause := context.Cause(ctx)
	if cause != testErr {
		t.Fatalf("expected cause %v, got %v", testErr, cause)
	}
	cancel1(nil) // cleanup (already cancelled, no-op)
}

func TestMergeCtx_CausePropagation_SecondParent(t *testing.T) {
	testErr := errors.New("second parent cause")

	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancelCause(context.Background())

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	cancel2(testErr)

	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after p2 cancel")
	}

	cause := context.Cause(ctx)
	if cause != testErr {
		t.Fatalf("expected cause %v, got %v", testErr, cause)
	}

	cancel1()
}

func TestMergeCtx_Deadline_Earliest(t *testing.T) {
	now := time.Now()
	early := now.Add(100 * time.Millisecond)
	late := now.Add(1 * time.Hour)

	p1, cancel1 := context.WithDeadline(context.Background(), late)
	p2, cancel2 := context.WithDeadline(context.Background(), early)
	defer cancel1()
	defer cancel2()

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	if !dl.Equal(early) {
		t.Fatalf("expected deadline %v, got %v", early, dl)
	}
}

func TestMergeCtx_Deadline_Expires(t *testing.T) {
	p, cancelP := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelP()

	ctx, cancel := MergeCtx(p)
	defer cancel()

	<-ctx.Done()

	if ctx.Err() == nil {
		t.Fatal("expected error after deadline expires")
	}
}

func TestMergeCtx_ValueLookup(t *testing.T) {
	type key struct{}
	p1 := context.WithValue(context.Background(), key{}, "from_p1")
	p2 := context.WithValue(context.Background(), key{}, "from_p2")

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	val := ctx.Value(key{})
	if val != "from_p1" {
		t.Fatalf("expected 'from_p1', got %v", val)
	}
}

func TestMergeCtx_ValueLookup_SecondParent(t *testing.T) {
	type key1 struct{}
	type key2 struct{}
	p1 := context.WithValue(context.Background(), key1{}, "val1")
	p2 := context.WithValue(context.Background(), key2{}, "val2")

	ctx, cancel := MergeCtx(p1, p2)
	defer cancel()

	if v := ctx.Value(key1{}); v != "val1" {
		t.Fatalf("expected 'val1', got %v", v)
	}
	if v := ctx.Value(key2{}); v != "val2" {
		t.Fatalf("expected 'val2', got %v", v)
	}
	if v := ctx.Value("missing"); v != nil {
		t.Fatalf("expected nil, got %v", v)
	}
}

func TestMergeCtx_ContextInterface(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	var ctx context.Context
	ctx, _ = MergeCtx(p1, p2)

	// Verify all Context interface methods work
	_ = ctx.Done()
	_ = ctx.Err()
	_, _ = ctx.Deadline()
	_ = ctx.Value("any")
}

func TestOrDone_SingleContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := orDone(ctx)

	cancel()
	<-done // should not block
}

func TestOrDone_MultipleContexts(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	done := orDone(p1, p2)

	cancel1()
	<-done // should not block
}

func TestOrDone_SecondContextCancels(t *testing.T) {
	p1, cancel1 := context.WithCancel(context.Background())
	p2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()

	done := orDone(p1, p2)

	cancel2()
	<-done // should not block
}
