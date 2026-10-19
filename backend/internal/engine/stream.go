package engine

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const copyBufSize = 32 * 1024

// halfCloseGrace is how long a connection may stay open in one direction
// after the other one finished, when no idle timeout is set (var for tests).
// With an idle timeout, that governs instead.
var halfCloseGrace = 5 * time.Minute

// copyBufs holds the copy buffers. A buffer is taken only while data is being
// moved, never while waiting for it: most proxied connections are quiet most
// of the time, and 64 KiB per idle connection added up to gigabytes.
var copyBufs = sync.Pool{New: func() any {
	b := make([]byte, copyBufSize)
	return &b
}}

// proxyStreams copies both ways until both sides are done or, when idle > 0,
// no data has moved in either direction for that long.
//
// When one side finishes sending (EOF), that is passed on with CloseWrite and
// the other direction keeps going, so protocols that half-close (send a
// request, shut down writing, wait for the answer) work. Any error, an idle
// timeout, or a connection that cannot half-close ends both directions.
func proxyStreams(a, b net.Conn, idle time.Duration) {
	defer a.Close()
	defer b.Close()

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	pump := func(dst, src net.Conn) {
		if !copyOneWay(dst, src, idle, &lastActivity) || !closeWrite(dst) {
			a.Close() // unblocks the opposite direction
			b.Close()
			return
		}
		if idle <= 0 {
			// The opposite direction reads from dst: don't wait for it forever
			dst.SetReadDeadline(time.Now().Add(halfCloseGrace))
		}
	}

	done := make(chan struct{})
	go func() { defer close(done); pump(b, a) }()
	pump(a, b) // this goroutine carries the other direction: one goroutine less per connection
	<-done
}

// copyOneWay copies src to dst until src ends. It reports whether it ended
// with a normal EOF, as opposed to an error or an idle timeout.
func copyOneWay(dst, src net.Conn, idle time.Duration, lastActivity *atomic.Int64) (clean bool) {
	if idle <= 0 {
		_, err := io.Copy(dst, src) // keeps the kernel splice fast path
		return err == nil
	}

	wait := readWaiter(src)
	for {
		src.SetReadDeadline(time.Now().Add(idle))

		var err error
		if wait != nil {
			err = wait() // blocks without holding a buffer
		}
		if err == nil {
			bp := copyBufs.Get().(*[]byte)
			var n int
			n, err = src.Read(*bp)
			if n > 0 {
				lastActivity.Store(time.Now().UnixNano())
				dst.SetWriteDeadline(time.Now().Add(idle))
				if _, werr := dst.Write((*bp)[:n]); werr != nil {
					copyBufs.Put(bp)
					return false
				}
			}
			copyBufs.Put(bp)
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, lastActivity.Load())) < idle {
				continue // quiet here, but the other direction is active
			}
			return err == io.EOF
		}
	}
}

// closeWrite shuts down the sending side of c, reporting whether that worked.
func closeWrite(c net.Conn) bool {
	cw, ok := c.(interface{ CloseWrite() error })
	return ok && cw.CloseWrite() == nil
}
