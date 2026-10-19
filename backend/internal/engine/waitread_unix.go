//go:build unix

package engine

import (
	"errors"
	"net"
	"os"
	"syscall"
)

// readWaiter returns a function that blocks until c has data, reached EOF or
// failed, or its read deadline passes, without needing a buffer. It returns
// nil when c does not expose its descriptor (e.g. a TLS connection, where
// the socket being quiet says nothing about buffered plaintext).
func readWaiter(c net.Conn) func() error {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	return func() error {
		var failure error
		err := rc.Read(func(fd uintptr) bool {
			// Peek instead of just waiting: data that arrived before this
			// call would otherwise be missed, as its readiness event is gone.
			var b [1]byte
			for {
				_, _, perr := syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
				switch {
				case perr == nil:
					return true // data, or EOF: the actual Read reports which
				case errors.Is(perr, syscall.EINTR):
					continue
				case errors.Is(perr, syscall.EAGAIN):
					return false
				}
				// A pending socket error (e.g. a reset) is consumed by this
				// call, so the next read would report plain EOF. Pass it on.
				failure = &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("recvfrom", perr)}
				return true
			}
		})
		if err == nil {
			err = failure
		}
		return err
	}
}
