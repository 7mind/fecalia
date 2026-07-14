package dnsresolve

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// dohMediaType is the RFC 8484 wire-format media type for both the request
// body and the expected response body.
const dohMediaType = "application/dns-message"

// dohTimeout bounds a single DoH POST round trip (request send + response
// read). It applies via http.Client.Timeout in addition to whatever deadline
// the caller's context already carries.
const dohTimeout = 5 * time.Second

// dohMaxMessageSize is the maximum wire size of a DNS message (RFC 1035
// SS4.2.2 / RFC 8484): the response body is capped to this to bound memory
// use against a malicious or misbehaving DoH server, regardless of any
// Content-Length header it claims.
const dohMaxMessageSize = 65535

// DoHResolver resolves hostnames via DNS-over-HTTPS (RFC 8484): it encodes A
// and AAAA queries with golang.org/x/net/dns/dnsmessage, POSTs each to a
// configured DoH URL, and extracts the answer addrs and minimum TTL.
//
// Residual leak: the DoH provider (and any network observer) sees the TLS
// SNI of the DoH URL's host and the timing/size of each request — DoH hides
// the query *content* from on-path observers, not the fact that this host is
// talking to that DoH provider.
type DoHResolver struct {
	url      string
	client   *http.Client
	dialHost string // non-empty: the bootstrap IP every dial is pinned to (NewDoHResolverWithBootstrap); empty: normal system-resolved dial
}

var _ Resolver = (*DoHResolver)(nil)

// NewDoHResolver returns a Resolver that POSTs DNS-over-HTTPS queries to
// rawURL (which must be an https:// URL). Certificate trust uses the
// platform's standard root CA store — there is no production knob to disable
// certificate verification.
func NewDoHResolver(rawURL string) (*DoHResolver, error) {
	return newDoHResolver(rawURL, nil, "")
}

// NewDoHResolverWithBootstrap returns a Resolver like NewDoHResolver, but
// dials bootstrapIP instead of resolving rawURL's host through the system
// dialer — the TLS ServerName (SNI) and HTTP Host header still use rawURL's
// original host, only the TCP connect address is pinned. Use this when
// rawURL's host is a hostname and its address is already known out-of-band
// (the BOOTSTRAP-IP invariant, Q33): resolving a private DoH resolver's own
// name via the system resolver before the first query would leak that lookup
// in plaintext, defeating the point of using a private resolver at all.
func NewDoHResolverWithBootstrap(rawURL, bootstrapIP string) (*DoHResolver, error) {
	return newDoHResolver(rawURL, nil, bootstrapIP)
}

// DialHost returns the bootstrap IP this resolver pins its TCP connect
// address to (set via NewDoHResolverWithBootstrap), or "" when dials go
// through the normal system-resolved path. Exposed so callers (and tests)
// can confirm which address a resolver actually dials, independent of the
// URL host it presents in the TLS SNI / HTTP Host header.
func (d *DoHResolver) DialHost() string {
	return d.dialHost
}

// newDoHResolver is the unexported constructor seam: a non-nil roots pool
// overrides the platform trust store with roots, for injecting a test CA
// (e.g. from httptest.NewTLSServer) — it exists ONLY for tests in this
// package. A non-empty bootstrapIP pins the TCP connect address (see
// NewDoHResolverWithBootstrap). Production code without a bootstrap goes
// through NewDoHResolver, which passes roots=nil, bootstrapIP="" and gets
// tls.Config's default (the system pool) and a normal system-resolved dial.
func newDoHResolver(rawURL string, roots *x509.CertPool, bootstrapIP string) (*DoHResolver, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("dnsresolve: invalid DoH URL %q: %w", rawURL, err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("dnsresolve: DoH URL %q must use https, got scheme %q", rawURL, parsed.Scheme)
	}

	transport := &http.Transport{
		// No proxy surprise: never route DoH queries through an
		// environment-configured (HTTP_PROXY/HTTPS_PROXY) proxy — a DoH
		// resolver silently redirected through an unexpected intermediary
		// would defeat the point of choosing a specific trusted provider.
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		},
		// HTTP/2 ok: Go only auto-negotiates h2 over a custom
		// TLSClientConfig when this is set explicitly.
		ForceAttemptHTTP2: true,
	}
	if bootstrapIP != "" {
		// Pin the TCP connect address to bootstrapIP, keeping the dialed
		// addr's PORT (http.Transport passes "host:port", defaulting to 443
		// for https). net/http derives the TLS ServerName and the HTTP Host
		// header from the request URL, not from the dial address, so SNI and
		// Host stay rawURL's original host — only the connect target moves.
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("dnsresolve: DoH bootstrap dial: invalid addr %q: %w", addr, err)
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(bootstrapIP, port))
		}
	}

	return &DoHResolver{
		url:      rawURL,
		dialHost: bootstrapIP,
		client: &http.Client{
			Transport: transport,
			Timeout:   dohTimeout,
		},
	}, nil
}

// Lookup implements Resolver. It queries A and AAAA for host and merges the
// results; a family that answers NXDOMAIN is tolerated as long as the other
// family answers. Any other per-family error (transport failure, non-200,
// malformed response) fails the whole lookup. An empty final addr set — both
// families NXDOMAIN, or a NOERROR/zero-answer (NODATA) response for both —
// is also an error (NXDomainError or NoDataError respectively), never a
// silent ([], nil).
func (d *DoHResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
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

	// An empty FINAL addr set is a failure, not a success: it covers both a
	// double-NXDOMAIN (below) and a NOERROR/zero-answer response (NODATA, or
	// a CNAME with no A/AAAA target) from one or both families. Returning
	// (nil, nil) here would diverge from SystemResolver, which surfaces a
	// no-such-host error from net.Resolver.LookupNetIP in the same
	// situation — callers must not see the two Resolver implementations
	// behave differently behind the same seam.
	if len(addrs) == 0 {
		if nxdomain != nil {
			return nil, 0, false, nxdomain
		}
		return nil, 0, false, &NoDataError{Endpoint: d.url, Host: host}
	}

	return addrs, minTTL, haveTTL, nil
}

// queryFamily runs a single A or AAAA query over DoH.
func (d *DoHResolver) queryFamily(ctx context.Context, host string, qtype dnsmessage.Type) ([]netip.Addr, time.Duration, bool, error) {
	query, err := buildQuery(host, qtype)
	if err != nil {
		return nil, 0, false, fmt.Errorf("dnsresolve: encoding DoH query for %q: %w", host, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(query))
	if err != nil {
		return nil, 0, false, fmt.Errorf("dnsresolve: building DoH request to %s: %w", d.url, err)
	}
	req.Header.Set("Content-Type", dohMediaType)
	req.Header.Set("Accept", dohMediaType)

	resp, err := d.client.Do(req)
	if err != nil {
		if isTimeoutErr(err) {
			return nil, 0, false, &TimeoutError{Endpoint: d.url, Err: err}
		}
		return nil, 0, false, fmt.Errorf("dnsresolve: DoH POST to %s failed: %w", d.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, false, &StatusError{URL: d.url, StatusCode: resp.StatusCode}
	}

	// Cap the read at dohMaxMessageSize+1: a DNS message can never legally
	// exceed dohMaxMessageSize bytes, so reading one extra byte lets us
	// detect and reject an oversized body without trusting an
	// attacker-controlled Content-Length or buffering an unbounded stream.
	body, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxMessageSize+1))
	if err != nil {
		if isTimeoutErr(err) {
			return nil, 0, false, &TimeoutError{Endpoint: d.url, Err: err}
		}
		return nil, 0, false, &MalformedResponseError{Endpoint: d.url, Err: err}
	}
	if len(body) > dohMaxMessageSize {
		return nil, 0, false, &MalformedResponseError{
			Endpoint: d.url,
			Err:      fmt.Errorf("response body exceeds max DNS message size of %d bytes", dohMaxMessageSize),
		}
	}

	return parseAnswer(d.url, body, host, qtype)
}

// isTimeoutErr reports whether err represents a request that exceeded its
// deadline (context deadline or http.Client/net.Dialer timeout).
func isTimeoutErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// StatusError reports a DoH provider responding with a non-200 HTTP status.
type StatusError struct {
	URL        string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("dnsresolve: DoH %s returned HTTP status %d", e.URL, e.StatusCode)
}
