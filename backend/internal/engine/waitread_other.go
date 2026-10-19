//go:build !unix

package engine

import "net"

// readWaiter is unsupported here: copying holds a buffer while it waits.
func readWaiter(net.Conn) func() error { return nil }
