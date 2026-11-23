package artifact

import (
	"errors"
	"io"
)

// LimitOutput bounds the bytes an operation can emit. Callers creating a file
// pair this with WriteNew so that an over-budget result is never published.
func LimitOutput(w io.Writer, bytes int64) io.Writer {
	return &boundedWriter{out: w, remaining: bytes}
}

type boundedWriter struct {
	out       io.Writer
	remaining int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > b.remaining {
		return 0, errors.New("result exceeds its output byte limit")
	}
	n, err := b.out.Write(p)
	b.remaining -= int64(n)
	return n, err
}
