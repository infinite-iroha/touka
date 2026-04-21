package touka

import (
	"bytes"
	"io"
	"testing"

	"github.com/WJQSERVER-STUDIO/go-utils/iox"
)

type benchmarkResetReader struct {
	data []byte
	off  int
}

func (r *benchmarkResetReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

func (r *benchmarkResetReader) Reset() {
	r.off = 0
}

type benchmarkDiscardWriter struct{}

func (benchmarkDiscardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

var benchmarkIOXResult int64
var benchmarkIOXBytes []byte

func BenchmarkIOXCopyComparison(b *testing.B) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	b.Run("io.Copy", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}
		w := benchmarkDiscardWriter{}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			n, err := io.Copy(w, r)
			if err != nil {
				b.Fatalf("io.Copy failed: %v", err)
			}
			benchmarkIOXResult = n
		}
	})

	b.Run("iox.Copy", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}
		w := benchmarkDiscardWriter{}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			n, err := iox.Copy(w, r)
			if err != nil {
				b.Fatalf("iox.Copy failed: %v", err)
			}
			benchmarkIOXResult = n
		}
	})
}

func BenchmarkIOXCopyBufferComparison(b *testing.B) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	b.Run("io.CopyBuffer", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}
		w := benchmarkDiscardWriter{}
		buf := make([]byte, 32*1024)

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			n, err := io.CopyBuffer(w, r, buf)
			if err != nil {
				b.Fatalf("io.CopyBuffer failed: %v", err)
			}
			benchmarkIOXResult = n
		}
	})

	b.Run("iox.CopyBuffer", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}
		w := benchmarkDiscardWriter{}
		buf := make([]byte, 32*1024)

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			n, err := iox.CopyBuffer(w, r, buf)
			if err != nil {
				b.Fatalf("iox.CopyBuffer failed: %v", err)
			}
			benchmarkIOXResult = n
		}
	})
}

func BenchmarkIOXReadAllComparison(b *testing.B) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	b.Run("io.ReadAll", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			data, err := io.ReadAll(r)
			if err != nil {
				b.Fatalf("io.ReadAll failed: %v", err)
			}
			benchmarkIOXBytes = data
		}
	})

	b.Run("iox.ReadAll", func(b *testing.B) {
		r := &benchmarkResetReader{data: payload}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			r.Reset()
			data, err := io.ReadAll(r)
			if err != nil {
				b.Fatalf("iox.ReadAll failed: %v", err)
			}
			benchmarkIOXBytes = data
		}
	})
}
