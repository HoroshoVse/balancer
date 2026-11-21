package engine

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"time"

	"github.com/balancer/backend/internal/models"
)

// replayConn returns the inspected ClientHello bytes before reading the socket.
type replayConn struct {
	net.Conn
	prefix []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.prefix) != 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func matchesClientSNI(routes, name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, route := range strings.Split(routes, ",") {
		if strings.ToLower(strings.TrimSpace(route)) == name {
			return true
		}
	}
	return false
}

func (l *LoadBalancerInstance) hasClientSNIRoutes() bool {
	for _, list := range [][]*models.BackendServer{l.backends.Load().([]*models.BackendServer), l.fallback.Load().([]*models.BackendServer)} {
		for _, b := range list {
			if strings.TrimSpace(b.ClientSNI) != "" {
				return true
			}
		}
	}
	return false
}

// peekClientSNI reads bounded TLS handshake records. Every byte is replayed unchanged.
// Non-TLS and malformed handshakes retain the existing backend selection.
func peekClientSNI(conn net.Conn) (string, []byte) {
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	var raw, handshake []byte
	for range 4 {
		h := make([]byte, 5)
		n, err := io.ReadFull(conn, h)
		raw = append(raw, h[:n]...)
		if err != nil || n != 5 || h[0] != 22 || h[1] != 3 {
			return "", raw
		}
		length := int(binary.BigEndian.Uint16(h[3:]))
		if length < 1 || length > 16384 {
			return "", raw
		}
		part := make([]byte, length)
		n, err = io.ReadFull(conn, part)
		raw = append(raw, part[:n]...)
		if err != nil {
			return "", raw
		}
		handshake = append(handshake, part...)
		if len(handshake) >= 4 {
			length = int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			if handshake[0] != 1 || length > 65536 {
				return "", raw
			}
			if len(handshake) >= 4+length {
				return clientHelloSNI(handshake), raw
			}
		}
	}
	return "", raw
}

func clientHelloSNI(record []byte) string {
	if len(record) < 4 || record[0] != 1 {
		return ""
	}
	n := int(record[1])<<16 | int(record[2])<<8 | int(record[3])
	if n > len(record)-4 {
		return ""
	}
	b := record[4 : 4+n]
	if len(b) < 35 { // version, random, session ID length
		return ""
	}
	b = b[34:]
	if len(b) < 1 || len(b) < 1+int(b[0])+2 {
		return ""
	}
	b = b[1+int(b[0]):]
	n = int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n+1 {
		return ""
	}
	b = b[2+n:]
	n = int(b[0])
	if len(b) < 1+n+2 {
		return ""
	}
	b = b[1+n:]
	n = int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return ""
	}
	b = b[2 : 2+n]
	for len(b) >= 4 {
		typ, size := binary.BigEndian.Uint16(b), int(binary.BigEndian.Uint16(b[2:]))
		if len(b) < 4+size {
			return ""
		}
		if typ == 0 {
			e := b[4 : 4+size]
			if len(e) < 5 || int(binary.BigEndian.Uint16(e)) != len(e)-2 {
				return ""
			}
			for e = e[2:]; len(e) >= 3; {
				nameSize := int(binary.BigEndian.Uint16(e[1:]))
				if len(e) < 3+nameSize {
					return ""
				}
				if e[0] == 0 && nameSize > 0 {
					return strings.ToLower(string(e[3 : 3+nameSize]))
				}
				e = e[3+nameSize:]
			}
			return ""
		}
		b = b[4+size:]
	}
	return ""
}
