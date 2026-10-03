package proxyproto

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

// roundTrip sends payload to a wrapped loopback listener and returns the
// host of the server-side RemoteAddr plus up to 5 bytes the server read.
func roundTrip(t *testing.T, trusted []string, payload string) (string, string, error) {
	t.Helper()
	prefixes, err := ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := Listener(raw, prefixes)
	defer ln.Close()

	go func() {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			return
		}
		_, _ = c.Write([]byte(payload))
		time.Sleep(200 * time.Millisecond)
		c.Close()
	}()

	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf, rerr := io.ReadAll(io.LimitReader(c, 5))
	return host, string(buf), rerr
}

func TestTrustedPeerHeaderApplied(t *testing.T) {
	host, got, err := roundTrip(t, []string{"127.0.0.0/8"}, "PROXY TCP4 203.0.113.9 10.0.0.1 51234 465\r\nEHLO ")
	if err != nil {
		t.Fatal(err)
	}
	if host != "203.0.113.9" || got != "EHLO " {
		t.Fatalf("host=%q data=%q", host, got)
	}
}

func TestTrustedPeerIPv6(t *testing.T) {
	host, _, _ := roundTrip(t, []string{"127.0.0.1"}, "PROXY TCP6 2001:db8::1 ::1 1 2\r\nx")
	if host != "2001:db8::1" {
		t.Fatalf("host=%q", host)
	}
}

func TestTrustedPeerMissingHeaderRejected(t *testing.T) {
	_, got, err := roundTrip(t, []string{"127.0.0.1"}, "EHLO x\r\n")
	if err == nil || got != "" {
		t.Fatalf("expected read error, got data=%q err=%v", got, err)
	}
}

func TestTrustedPeerMalformedHeaderRejected(t *testing.T) {
	for _, h := range []string{
		"PROXY TCP4 notanip 10.0.0.1 1 2\r\n",
		"PROXY TCP4 2001:db8::1 10.0.0.1 1 2\r\n",
		"PROXY TCP4 1.2.3.4 10.0.0.1 99999 2\r\n",
		"PROXY TCP4 1.2.3.4 10.0.0.1 1 2\n",
	} {
		if _, _, err := roundTrip(t, []string{"127.0.0.1"}, h+"hello"); err == nil {
			t.Errorf("header %q was accepted", h)
		}
	}
}

func TestUnknownKeepsProxyAddress(t *testing.T) {
	host, got, err := roundTrip(t, []string{"127.0.0.1"}, "PROXY UNKNOWN\r\nhello")
	if err != nil || host != "127.0.0.1" || got != "hello" {
		t.Fatalf("host=%q data=%q err=%v", host, got, err)
	}
}

// An untrusted peer must not be able to spoof its address.
func TestUntrustedPeerHeaderIgnored(t *testing.T) {
	host, got, err := roundTrip(t, []string{"192.0.2.0/24"}, "PROXY TCP4 203.0.113.9 10.0.0.1 1 2\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || got != "PROXY" {
		t.Fatalf("host=%q data=%q", host, got)
	}
}

func TestParseTrusted(t *testing.T) {
	p, err := ParseTrusted([]string{"10.0.0.0/8", "::1", "172.18.0.5"})
	if err != nil || len(p) != 3 || p[2] != netip.MustParsePrefix("172.18.0.5/32") {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := ParseTrusted([]string{"bogus"}); err == nil {
		t.Fatal("expected error")
	}
}
