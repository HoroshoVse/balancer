package engine

import (
	"net"
	"strconv"

	"github.com/pires/go-proxyproto"
)

// backendAddr formats host:port, bracketing IPv6 literals.
func backendAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func addrParts(a net.Addr) (ip net.IP, port int, udp bool) {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP, v.Port, false
	case *net.UDPAddr:
		return v.IP, v.Port, true
	case nil:
		return nil, 0, false
	}
	host, p, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil, 0, false
	}
	port, _ = strconv.Atoi(p)
	return net.ParseIP(host), port, false
}

// buildProxyHeader describes a client connection src -> dst for the backend.
// dst is coerced to the client's address family, as the protocol requires.
func buildProxyHeader(version int, src, dst net.Addr) *proxyproto.Header {
	if version != 1 {
		version = 2
	}
	header := &proxyproto.Header{Version: byte(version), Command: proxyproto.PROXY}

	srcIP, srcPort, udp := addrParts(src)
	dstIP, dstPort, _ := addrParts(dst)
	if srcIP == nil {
		header.Command = proxyproto.LOCAL
		header.TransportProtocol = proxyproto.UNSPEC
		return header
	}

	v4 := srcIP.To4() != nil
	if v4 {
		srcIP = srcIP.To4()
		if d := dstIP.To4(); d != nil {
			dstIP = d
		} else {
			dstIP = net.IPv4zero.To4()
		}
	} else if dstIP == nil {
		dstIP = net.IPv6unspecified
	} else {
		dstIP = dstIP.To16()
	}

	switch {
	case udp && v4:
		header.TransportProtocol = proxyproto.UDPv4
	case udp:
		header.TransportProtocol = proxyproto.UDPv6
	case v4:
		header.TransportProtocol = proxyproto.TCPv4
	default:
		header.TransportProtocol = proxyproto.TCPv6
	}

	if udp {
		header.SourceAddr = &net.UDPAddr{IP: srcIP, Port: srcPort}
		header.DestinationAddr = &net.UDPAddr{IP: dstIP, Port: dstPort}
	} else {
		header.SourceAddr = &net.TCPAddr{IP: srcIP, Port: srcPort}
		header.DestinationAddr = &net.TCPAddr{IP: dstIP, Port: dstPort}
	}
	return header
}
