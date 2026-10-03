// Package proxyproto accepts the PROXY protocol v1 header that reverse
// proxies such as nginx (proxy_protocol on) prepend to a TCP stream, so the
// application sees the real client address.
//
// The header is honoured only for connections whose direct peer is in the
// trusted set. Every other connection is passed through untouched, so a
// forged header from an untrusted client is just malformed SMTP.
package proxyproto

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxHeaderLen is the longest legal v1 line, CRLF included.
	maxHeaderLen = 107
	// headerTimeout bounds how long a trusted peer may take to send it.
	headerTimeout = 10 * time.Second
)

// ParseTrusted parses a list of IP addresses and CIDR ranges.
func ParseTrusted(entries []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", e)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Listener wraps l so that connections from trusted peers must start with a
// PROXY v1 header. With an empty trusted set it returns l unchanged.
func Listener(l net.Listener, trusted []netip.Prefix) net.Listener {
	if len(trusted) == 0 {
		return l
	}
	return &listener{Listener: l, trusted: trusted}
}

type listener struct {
	net.Listener
	trusted []netip.Prefix
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if !l.isTrusted(c.RemoteAddr()) {
		return c, nil
	}
	return &conn{Conn: c, br: bufio.NewReaderSize(c, 4096)}, nil
}

func (l *listener) isTrusted(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	for _, p := range l.trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// conn parses the header lazily, on first Read or RemoteAddr, so a slow peer
// never blocks the accept loop.
type conn struct {
	net.Conn
	br     *bufio.Reader
	once   sync.Once
	remote net.Addr
	err    error
}

func (c *conn) handshake() {
	c.once.Do(func() {
		_ = c.Conn.SetReadDeadline(time.Now().Add(headerTimeout))
		c.remote, c.err = readHeader(c.br)
		_ = c.Conn.SetReadDeadline(time.Time{})
	})
}

func (c *conn) Read(p []byte) (int, error) {
	c.handshake()
	if c.err != nil {
		return 0, c.err
	}
	return c.br.Read(p)
}

// RemoteAddr returns the client address from the header. If the header was
// invalid it falls back to the proxy's own address; Read then fails, so the
// connection never reaches the application.
func (c *conn) RemoteAddr() net.Addr {
	c.handshake()
	if c.err != nil || c.remote == nil {
		return c.Conn.RemoteAddr()
	}
	return c.remote
}

var errBadHeader = errors.New("proxyproto: invalid PROXY protocol header")

// readHeader consumes one PROXY v1 line. "PROXY UNKNOWN" yields a nil
// address, meaning the proxy's own address stays in effect.
func readHeader(br *bufio.Reader) (net.Addr, error) {
	var line []byte
	for len(line) < maxHeaderLen {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
	}
	s := string(line)
	if !strings.HasSuffix(s, "\r\n") || !strings.HasPrefix(s, "PROXY ") {
		return nil, errBadHeader
	}
	f := strings.Fields(strings.TrimSuffix(s, "\r\n"))
	if len(f) >= 2 && f[1] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, errBadHeader
	}
	ip, err := netip.ParseAddr(f[2])
	if err != nil || (f[1] == "TCP4") != ip.Is4() {
		return nil, errBadHeader
	}
	port, err := strconv.Atoi(f[4])
	if err != nil || port < 0 || port > 65535 {
		return nil, errBadHeader
	}
	return &net.TCPAddr{IP: ip.AsSlice(), Port: port}, nil
}
