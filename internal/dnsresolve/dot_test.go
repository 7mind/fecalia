package dnsresolve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// generateDoTTestCert returns a self-signed leaf certificate (and its
// x509.CertPool for trust injection) for dnsName, mimicking how
// httptest.NewTLSServer mints its certificate but for a raw TCP+TLS listener
// (net/http/httptest only covers HTTP transports).
func generateDoTTestCert(t *testing.T, dnsName string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{dnsName},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating test certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing test certificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// dotFrameHandler answers one length-prefixed DNS query read from conn.
type dotFrameHandler func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte

// startDoTTestListener starts a hermetic TLS listener speaking RFC
// 7858-framed DNS: it accepts one connection, reads one length-prefixed
// query, invokes handle to build the (already-packed) response, and writes
// it back length-prefixed. It returns the listener address and a stop func.
func startDoTTestListener(t *testing.T, cert tls.Certificate, handle dotFrameHandler) (addr string, stop func()) {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("starting DoT test listener: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveDoTTestConn(t, conn, handle)
		}
	}()

	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

func serveDoTTestConn(t *testing.T, conn net.Conn, handle dotFrameHandler) {
	defer conn.Close()

	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}

	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return
	}
	if len(msg.Questions) != 1 {
		return
	}

	resp := handle(t, msg, msg.Questions[0])
	if resp == nil {
		return
	}

	frame := make([]byte, 2+len(resp))
	binary.BigEndian.PutUint16(frame, uint16(len(resp)))
	copy(frame[2:], resp)
	_, _ = conn.Write(frame)
}

func packDoTAnswer(t *testing.T, q dnsmessage.Question, answers []dnsmessage.Resource) []byte {
	t.Helper()
	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:           true,
			RecursionAvailable: true,
			RCode:              dnsmessage.RCodeSuccess,
		},
		Questions: []dnsmessage.Question{q},
		Answers:   answers,
	}
	buf, err := resp.Pack()
	if err != nil {
		t.Fatalf("packing DoT response: %v", err)
	}
	return buf
}

func TestDoTResolverLookupReturnsExpectedAddrsAndMinTTL(t *testing.T) {
	const serverName = "dot.example.com"
	cert, pool := generateDoTTestCert(t, serverName)

	addr, stop := startDoTTestListener(t, cert, func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte {
		switch q.Type {
		case dnsmessage.TypeA:
			return packDoTAnswer(t, q, []dnsmessage.Resource{
				aResource(t, q, [4]byte{198, 51, 100, 1}, 300),
				aResource(t, q, [4]byte{198, 51, 100, 2}, 120),
			})
		case dnsmessage.TypeAAAA:
			return packDoTAnswer(t, q, []dnsmessage.Resource{
				aaaaResource(t, q, [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}, 60),
			})
		default:
			t.Fatalf("unexpected query type %v", q.Type)
			return nil
		}
	})
	defer stop()

	// Two families means two connections (one per queryFamily call), so the
	// listener must accept twice. startDoTTestListener's Accept loop handles
	// this already: each Lookup family gets a fresh conn.
	r, err := newDoTResolver(addr, serverName, pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, minTTL, ttlOk, err := r.Lookup(ctx, "example.com")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if !ttlOk {
		t.Fatal("Lookup: ttlOk = false, want true")
	}
	if minTTL != 60*time.Second {
		t.Fatalf("Lookup: minTTL = %v, want 60s", minTTL)
	}

	want := map[netip.Addr]bool{
		netip.MustParseAddr("198.51.100.1"): true,
		netip.MustParseAddr("198.51.100.2"): true,
		netip.MustParseAddr("2001:db8::2"):  true,
	}
	if len(addrs) != len(want) {
		t.Fatalf("Lookup: got %d addrs %v, want %d", len(addrs), addrs, len(want))
	}
	for _, a := range addrs {
		if !want[a] {
			t.Fatalf("Lookup: unexpected addr %v in %v", a, addrs)
		}
	}
}

// TestDoTResolverWrongServerNameFailsVerification covers certificate-name
// verification: the resolver is configured with a serverName that does not
// match the listener's certificate SAN, so the TLS handshake must fail
// rather than silently trusting the connection.
func TestDoTResolverWrongServerNameFailsVerification(t *testing.T) {
	cert, pool := generateDoTTestCert(t, "dot.example.com")

	addr, stop := startDoTTestListener(t, cert, func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte {
		t.Fatal("handler should not be reached: TLS handshake must fail first")
		return nil
	})
	defer stop()

	r, err := newDoTResolver(addr, "wrong.example.com", pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, _, err = r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected certificate verification error, got nil")
	}
	var hostErr x509.HostnameError
	if !errors.As(err, &hostErr) {
		t.Fatalf("Lookup: err = %v (%T), want an x509.HostnameError in the chain", err, err)
	}
}

func TestDoTResolverTimeout(t *testing.T) {
	const serverName = "dot.example.com"
	cert, pool := generateDoTTestCert(t, serverName)

	blockCh := make(chan struct{})
	addr, stop := startDoTTestListener(t, cert, func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte {
		<-blockCh // never respond within the test's short context deadline
		return nil
	})
	// stop() closes the listener but doesn't need blockCh released first:
	// the blocked connection handler goroutine leaks harmlessly for the
	// remainder of the test process, same tradeoff doh_test.go avoids only
	// because httptest.Server.Close() explicitly waits for handlers — here
	// we just release it via defer ordering to keep -race clean.
	defer func() {
		close(blockCh)
		stop()
	}()

	r, err := newDoTResolver(addr, serverName, pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, _, err = r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected timeout error, got nil")
	}
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Lookup: err = %v (%T), want *TimeoutError", err, err)
	}
}

// TestDoTResolverTruncatedFrame covers a server that declares a length
// prefix and then closes the connection before delivering that many bytes:
// the RFC 7858 framing discipline must surface a typed error, not panic or
// hang.
func TestDoTResolverTruncatedFrame(t *testing.T) {
	const serverName = "dot.example.com"
	cert, pool := generateDoTTestCert(t, serverName)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("starting DoT test listener: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				var lenBuf [2]byte
				if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
					return
				}
				// Declare a large body, but only write a few bytes then
				// close: a truncated frame.
				frame := make([]byte, 2, 5)
				binary.BigEndian.PutUint16(frame, 100)
				frame = append(frame, 0x01, 0x02, 0x03)
				_, _ = conn.Write(frame)
			}(conn)
		}
	}()
	defer func() {
		ln.Close()
		<-done
	}()

	r, err := newDoTResolver(ln.Addr().String(), serverName, pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, _, err = r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected error for truncated frame, got nil")
	}
	var malformed *MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("Lookup: err = %v (%T), want *MalformedResponseError", err, err)
	}
}

// TestDoTResolverDoubleNODATAIsError mirrors
// TestDoHResolverDoubleNODATAIsError: Lookup must return a typed error
// rather than ([], nil) when both families answer NOERROR with zero
// records, matching DoHResolver's error taxonomy exactly.
func TestDoTResolverDoubleNODATAIsError(t *testing.T) {
	const serverName = "dot.example.com"
	cert, pool := generateDoTTestCert(t, serverName)

	addr, stop := startDoTTestListener(t, cert, func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte {
		return packDoTAnswer(t, q, nil) // NOERROR, zero answers
	})
	defer stop()

	r, err := newDoTResolver(addr, serverName, pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, _, _, err := r.Lookup(ctx, "nodata.example.com")
	if err == nil {
		t.Fatalf("Lookup: expected error for double-NODATA, got addrs=%v, nil error", addrs)
	}
	var noData *NoDataError
	if !errors.As(err, &noData) {
		t.Fatalf("Lookup: err = %v (%T), want *NoDataError", err, err)
	}
}

func TestNewDoTResolverRejectsEmptyServer(t *testing.T) {
	if _, err := NewDoTResolver(""); err == nil {
		t.Fatal("NewDoTResolver(\"\"): expected error, got nil")
	}
}

// TestNewDoTResolverWithBootstrapDialAddr is the plain constructor test (no
// live dial) for the exported NewDoTResolverWithBootstrap: the stored dial
// target must be bootstrapIP:853 — the fixed IANA DoT port dnsresolve always
// dials — never the hostname.
func TestNewDoTResolverWithBootstrapDialAddr(t *testing.T) {
	r, err := NewDoTResolverWithBootstrap("resolver.example.com", "198.51.100.1")
	if err != nil {
		t.Fatalf("NewDoTResolverWithBootstrap: unexpected error: %v", err)
	}
	if want := "198.51.100.1:853"; r.DialAddr() != want {
		t.Fatalf("DialAddr() = %q, want %q", r.DialAddr(), want)
	}
}

// TestNewDoTResolverWithBootstrapDialsBootstrapIP is the hermetic construct
// test for the BOOTSTRAP-IP invariant (Q33): serverName names a HOSTNAME
// that does not exist in DNS, so if the resolver dialed it via the system
// resolver the connection would fail before any TLS handshake happens. It
// instead dials bootstrapIP:port (a real hermetic TLS listener bound there,
// mirroring NewDoTResolverWithBootstrap's addr construction but at an
// ephemeral test port since the exported constructor's port is fixed at
// 853), proving the dial address — not the server name — determines the TCP
// connect target, while the listener's certificate is issued for the
// hostname to also confirm the TLS ServerName / SNI is untouched.
func TestNewDoTResolverWithBootstrapDialsBootstrapIP(t *testing.T) {
	const unresolvableHost = "dot-resolver.invalid.wanbond-test"
	cert, pool := generateDoTTestCert(t, unresolvableHost)

	addr, stop := startDoTTestListener(t, cert, func(t *testing.T, msg dnsmessage.Message, q dnsmessage.Question) []byte {
		switch q.Type {
		case dnsmessage.TypeA:
			return packDoTAnswer(t, q, []dnsmessage.Resource{aResource(t, q, [4]byte{192, 0, 2, 43}, 30)})
		case dnsmessage.TypeAAAA:
			resp := dnsmessage.Message{Header: dnsmessage.Header{Response: true, RCode: dnsmessage.RCodeNameError}, Questions: []dnsmessage.Question{q}}
			buf, err := resp.Pack()
			if err != nil {
				t.Fatalf("packing NXDOMAIN response: %v", err)
			}
			return buf
		default:
			t.Fatalf("unexpected query type %v", q.Type)
			return nil
		}
	})
	defer stop()

	bootstrapIP, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitting listener addr: %v", err)
	}

	// Mirrors NewDoTResolverWithBootstrap's addr construction
	// (net.JoinHostPort(bootstrapIP, dotPort)) at the listener's ephemeral
	// port instead of the fixed 853, since binding 853 needs root.
	r, err := newDoTResolver(net.JoinHostPort(bootstrapIP, port), unresolvableHost, pool)
	if err != nil {
		t.Fatalf("newDoTResolver: unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, _, _, err := r.Lookup(ctx, "example.com")
	if err != nil {
		t.Fatalf("Lookup: unexpected error (bootstrap dial should have reached the hermetic listener without any DNS lookup of %q): %v", unresolvableHost, err)
	}
	if len(addrs) != 1 || addrs[0] != netip.MustParseAddr("192.0.2.43") {
		t.Fatalf("Lookup: got %v, want [192.0.2.43]", addrs)
	}
}
