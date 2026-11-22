package engine

import (
	"io"
	"sync/atomic"
)

// countingBody records bytes as they are actually read by the transport or
// reverse proxy, including long streaming HTTP responses.
type countingBody struct {
	io.ReadCloser
	counter *uint64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		atomic.AddUint64(b.counter, uint64(n))
	}
	return n, err
}
