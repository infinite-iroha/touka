package touka

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

type benchmarkReadSeeker struct {
	data []byte
	off  int
}

func (r *benchmarkReadSeeker) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *benchmarkReadSeeker) Reset() {
	r.off = 0
}

type benchmarkResponseWriter struct {
	header http.Header
	status int
	size   int
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	return &benchmarkResponseWriter{header: make(http.Header)}
}

func (w *benchmarkResponseWriter) Header() http.Header {
	return w.header
}

func (w *benchmarkResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *benchmarkResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.size += len(p)
	return len(p), nil
}

func (w *benchmarkResponseWriter) Flush() {}

func (w *benchmarkResponseWriter) Status() int {
	return w.status
}

func (w *benchmarkResponseWriter) Size() int {
	return w.size
}

func (w *benchmarkResponseWriter) Written() bool {
	return w.status != 0
}

func (w *benchmarkResponseWriter) IsHijacked() bool {
	return false
}

func (w *benchmarkResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

func (w *benchmarkResponseWriter) reset() {
	clear(w.header)
	w.status = 0
	w.size = 0
}

var benchmarkReverseProxySink int

func BenchmarkReverseProxyCopyResponse(b *testing.B) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	proxy := newReverseProxyHandler(ReverseProxyConfig{})
	dst := newBenchmarkResponseWriter()
	src := &benchmarkReadSeeker{data: body}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		dst.reset()
		src.Reset()
		if err := proxy.copyResponse(dst, src, 0); err != nil {
			b.Fatalf("copyResponse failed: %v", err)
		}
	}

	benchmarkReverseProxySink = dst.Size()
}

func BenchmarkReverseProxyAvailableUpstreams(b *testing.B) {
	proxy := &reverseProxyHandler{
		upstreams: []*reverseProxyUpstream{
			{key: "a"},
			{key: "b"},
			{key: "c"},
			{key: "d"},
		},
		config: ReverseProxyConfig{
			PassiveHealth: ReverseProxyPassiveHealthConfig{
				FailDuration: time.Minute,
				MaxFails:     3,
			},
		},
	}

	now := time.Now()
	proxy.upstreams[0].failures = []time.Time{now.Add(-30 * time.Second)}
	proxy.upstreams[1].failures = []time.Time{now.Add(-20 * time.Second), now.Add(-10 * time.Second)}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		benchmarkReverseProxySink = len(proxy.availableUpstreams(now, nil))
	}
}

func TestReverseProxyCopyResponseWithoutBufferPool(t *testing.T) {
	proxy := newReverseProxyHandler(ReverseProxyConfig{})
	dst := newBenchmarkResponseWriter()
	src := bytes.NewBufferString("hello, reverse proxy")

	if err := proxy.copyResponse(dst, src, 0); err != nil {
		t.Fatalf("copyResponse failed: %v", err)
	}

	if got, want := dst.Size(), len("hello, reverse proxy"); got != want {
		t.Fatalf("expected %d bytes copied, got %d", want, got)
	}
}

type fixedLenBufferPool struct {
	buf []byte
}

func (p *fixedLenBufferPool) Get() []byte {
	return p.buf
}

func (p *fixedLenBufferPool) Put(buf []byte) {
	p.buf = buf
}

type recordingReader struct {
	chunk int
	reads []int
	left  int
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(r.chunk, len(p), r.left)
	if n == 0 {
		return 0, errors.New("reader received zero-length buffer")
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.left -= n
	r.reads = append(r.reads, len(p))
	return n, nil
}

func TestReverseProxyCopyResponseRespectsCustomBufferLength(t *testing.T) {
	pool := &fixedLenBufferPool{buf: make([]byte, 8, 32*1024)}
	proxy := newReverseProxyHandler(ReverseProxyConfig{BufferPool: pool})
	dst := newBenchmarkResponseWriter()
	src := &recordingReader{chunk: 8, left: 24}

	if err := proxy.copyResponse(dst, src, 0); err != nil {
		t.Fatalf("copyResponse failed: %v", err)
	}

	if len(src.reads) == 0 {
		t.Fatal("expected reader to be used")
	}
	for _, size := range src.reads {
		if size != 8 {
			t.Fatalf("expected custom buffer length 8 to be preserved, got read size %d", size)
		}
	}
}

var _ io.Writer = (*benchmarkResponseWriter)(nil)
