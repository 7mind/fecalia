package dnsresolve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dotPort is the IANA-assigned DNS-over-TLS port (RFC 7858 SS3).
const dotPort = "853"

// dotTimeout bounds a single DoT lookup (dial + TLS handshake + query write +
// response read), in addition to whatever deadline the caller's context
// already carries.
const dotTimeout = 5 * time.Second

// dotMaxMessageSize mirrors dohMaxMessageSize: RFC 1035 SS4.2.2 bounds any
// legal DNS message to this size, so a length prefix claiming more is
// rejected without being trusted or buffered.
const dotMaxMessageSize = 65535

// DoTResolver resolves hostnames via DNS-over-TLS (RFC 7858): it dials the
// configured server on port 853 with crypto/tls (verifying the presented
// certificate against the server name), and exchanges dnsmessage-encoded A
// and AAAA queries framed with the RFC 7858 2-byte big-endian length prefix.
//
// v1 dials one fresh TLS connection per family per Lookup call — lookups run
// at seconds cadence, not hot-path, so connection reuse is not worth the
// added complexity yet.
type DoTResolver struct {
	addr      string // dial address, "host:853" (or a test-injected host:port)
	tlsConfig *tls.Config
}

var _ Resolver = (*DoTResolver)(nil)

// NewDoTResolver returns a Resolver that queries server over DNS-over-TLS on
// port 853; server is used both as the dial target and as the TLS server
// name verified against the presented certificate. Certificate trust uses
// the platform's standard root CA store — there is no production knob to
// disable certificate verification.
func NewDoTResolver(server string) (*DoTResolver, error) {
	return newDoTResolver(net.JoinHostPort(server, dotPort), server, nil)
}

// NewDoTResolverWithBootstrap returns a Resolver like NewDoTResolver, but
// dials bootstrapIP:853 instead of resolving server through the system
// dialer, while still verifying the presented certificate against server as
// the TLS server name. Use this when server is a hostname and its address is
// already known out-of-band (the BOOTSTRAP-IP invariant, Q33): resolving a
// private DoT resolver's own name via the system resolver before the first
// query would leak that lookup in plaintext, defeating the point of using a
// private resolver at all.
func NewDoTResolverWithBootstrap(server, bootstrapIP string) (*DoTResolver, error) {
	return newDoTResolver(net.JoinHostPort(bootstrapIP, dotPort), server, nil)
}

// DialAddr returns the dial target ("host:port") this resolver connects to
// for every lookup — bootstrapIP:853 when constructed via
// NewDoTResolverWithBootstrap, server:853 otherwise. Exposed so callers (and
// tests) can confirm which address a resolver actually dials, independent of
// the TLS server name it verifies against.
func (d *DoTResolver) DialAddr() string {
	return d.addr
}

// newDoTResolver is the unexported constructor seam: addr is the dial
// target (host:port), serverName is the TLS server name verified against
// the presented certificate, and a non-nil roots pool overrides the
// platform trust store with roots. It exists ONLY for tests in this
// package, mirroring newDoHResolver — tests dial a loopback host:port while
// still exercising real certificate-name verification by passing a
// serverName that differs from addr's host.
func newDoTResolver(addr, serverName string, roots *x509.CertPool) (*DoTResolver, error) {
	if serverName == "" {
		return nil, errors.New("dnsresolve: DoT server name must not be empty")
	}
	return &DoTResolver{
		addr: addr,
		tlsConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: serverName,
			MinVersion: tls.VersionTLS12,
		},
	}, nil
}

// Lookup implements Resolver. It queries A and AAAA for host and merges the
// results; a family that answers NXDOMAIN is tolerated as long as the other
// family answers. Any other per-family error (dial/TLS failure, timeout,
// truncated frame, malformed response) fails the whole lookup. An empty
// final addr set — both families NXDOMAIN, or a NOERROR/zero-answer
// (NODATA) response for both — is also an error (NXDomainError or
// NoDataError respectively), never a silent ([], nil). Matches DoHResolver's
// error taxonomy and empty-result handling exactly.
func (d *DoTResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	var (
		addrs    []netip.Addr
		minTTL   time.Duration
		haveTTL  bool
		nxdomain error
	)

	for _, qtype := range [...]dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		famAddrs, famMinTTL, famHaveTTL, err := d.queryFamily(ctx, host, qtype)
		if err != nil {
			var nx *NXDomainError
			if errors.As(err, &nx) {
				nxdomain = err
				continue
			}
			return nil, 0, false, err
		}

		addrs = append(addrs, famAddrs...)
		if famHaveTTL && (!haveTTL || famMinTTL < minTTL) {
			minTTL = famMinTTL
			haveTTL = true
		}
	}

	if len(addrs) == 0 {
		if nxdomain != nil {
			return nil, 0, false, nxdomain
		}
		return nil, 0, false, &NoDataError{Endpoint: d.addr, Host: host}
	}

	return addrs, minTTL, haveTTL, nil
}

// queryFamily runs a single A or AAAA query over one fresh DoT connection:
// dial + TLS handshake, write the length-prefixed query, read the
// length-prefixed answer.
func (d *DoTResolver) queryFamily(ctx context.Context, host string, qtype dnsmessage.Type) ([]netip.Addr, time.Duration, bool, error) {
	query, err := buildQuery(host, qtype)
	if err != nil {
		return nil, 0, false, fmt.Errorf("dnsresolve: encoding DoT query for %q: %w", host, err)
	}

	// Bound the whole exchange to dotTimeout, further bounded by the
	// caller's context deadline when it is sooner.
	deadline := time.Now().Add(dotTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Deadline: deadline},
		Config:    d.tlsConfig,
	}
	conn, err := dialer.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		if isTimeoutErr(err) {
			return nil, 0, false, &TimeoutError{Endpoint: d.addr, Err: err}
		}
		return nil, 0, false, fmt.Errorf("dnsresolve: DoT dial to %s failed: %w", d.addr, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(deadline); err != nil {
		return nil, 0, false, fmt.Errorf("dnsresolve: setting DoT connection deadline: %w", err)
	}

	if err := writeDoTFrame(conn, query); err != nil {
		if isTimeoutErr(err) {
			return nil, 0, false, &TimeoutError{Endpoint: d.addr, Err: err}
		}
		return nil, 0, false, fmt.Errorf("dnsresolve: writing DoT query to %s failed: %w", d.addr, err)
	}

	body, err := readDoTFrame(conn)
	if err != nil {
		if isTimeoutErr(err) {
			return nil, 0, false, &TimeoutError{Endpoint: d.addr, Err: err}
		}
		return nil, 0, false, &MalformedResponseError{Endpoint: d.addr, Err: err}
	}

	return parseAnswer(d.addr, body, host, qtype)
}

// writeDoTFrame writes msg to w prefixed with its RFC 7858 2-byte
// big-endian length.
func writeDoTFrame(w io.Writer, msg []byte) error {
	if len(msg) > dotMaxMessageSize {
		return fmt.Errorf("dnsresolve: DoT query exceeds max DNS message size of %d bytes", dotMaxMessageSize)
	}
	frame := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(frame, uint16(len(msg)))
	copy(frame[2:], msg)
	_, err := w.Write(frame)
	return err
}

// readDoTFrame reads one RFC 7858 length-prefixed DNS message from r. A
// connection that closes (or times out) before the declared length is fully
// read yields a wrapped io.ErrUnexpectedEOF/timeout, which the caller
// classifies into a typed TimeoutError or MalformedResponseError.
func readDoTFrame(r io.Reader) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		if isTimeoutErr(err) {
			return nil, err
		}
		return nil, fmt.Errorf("truncated length prefix: %w", err)
	}

	n := binary.BigEndian.Uint16(lenBuf[:])
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if isTimeoutErr(err) {
			return nil, err
		}
		return nil, fmt.Errorf("truncated %d-byte message: %w", n, err)
	}
	return body, nil
}
