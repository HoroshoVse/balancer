//go:build unix

package engine

import (
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestReadWaiter(t *testing.T) {
	waitReturns := func(t *testing.T, wait func() error, within time.Duration) error {
		t.Helper()
		res := make(chan error, 1)
		go func() { res <- wait() }()
		select {
		case err := <-res:
			return err
		case <-time.After(within):
			t.Fatalf("wait did not return within %v", within)
			return nil
		}
	}

	t.Run("data that arrived before waiting", func(t *testing.T) {
		client, server := tcpPair(t)
		defer client.Close()
		defer server.Close()
		client.Write([]byte("x"))
		time.Sleep(100 * time.Millisecond) // the readiness event has come and gone
		wait := readWaiter(server)
		if wait == nil {
			t.Fatal("no waiter for a TCP connection")
		}
		server.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := waitReturns(t, wait, time.Second); err != nil {
			t.Fatalf("wait = %v, want it to see the pending byte", err)
		}
	})

	t.Run("data that arrives while waiting", func(t *testing.T) {
		client, server := tcpPair(t)
		defer client.Close()
		defer server.Close()
		wait := readWaiter(server)
		server.SetReadDeadline(time.Now().Add(5 * time.Second))
		go func() { time.Sleep(150 * time.Millisecond); client.Write([]byte("x")) }()
		if err := waitReturns(t, wait, 2*time.Second); err != nil {
			t.Fatalf("wait = %v", err)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		client, server := tcpPair(t)
		defer client.Close()
		defer server.Close()
		wait := readWaiter(server)
		server.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		err := waitReturns(t, wait, 2*time.Second)
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("wait = %v, want a timeout", err)
		}
	})

	t.Run("EOF", func(t *testing.T) {
		client, server := tcpPair(t)
		defer server.Close()
		wait := readWaiter(server)
		server.SetReadDeadline(time.Now().Add(5 * time.Second))
		go func() { time.Sleep(100 * time.Millisecond); client.Close() }()
		if err := waitReturns(t, wait, 2*time.Second); err != nil {
			t.Fatalf("wait = %v, want it to return so Read can report EOF", err)
		}
		if _, err := server.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("read after wait = %v, want EOF", err)
		}
	})

	t.Run("reset is reported as an error, not as EOF", func(t *testing.T) {
		client, server := tcpPair(t)
		defer server.Close()
		wait := readWaiter(server)
		server.SetReadDeadline(time.Now().Add(5 * time.Second))
		go func() {
			time.Sleep(100 * time.Millisecond)
			client.(*net.TCPConn).SetLinger(0)
			client.Close() // sends a reset
		}()
		err := waitReturns(t, wait, 2*time.Second)
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("wait = %v, want connection reset", err)
		}
	})

	t.Run("no waiter for non-descriptor connections", func(t *testing.T) {
		a, _ := pipePair(t)
		if readWaiter(a) != nil {
			t.Fatal("net.Pipe should use the plain path")
		}
	})
}
