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
	"strings"
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
	url    string
	client *http.Client
}

var _ Resolver = (*DoHResolver)(nil)

// NewDoHResolver returns a Resolver that POSTs DNS-over-HTTPS queries to
// rawURL (which must be an https:// URL). Certificate trust uses the
// platform's standard root CA store — there is no production knob to disable
// certificate verification.
func NewDoHResolver(rawURL string) (*DoHResolver, error) {
	return newDoHResolver(rawURL, nil)
}

// newDoHResolver is the unexported constructor seam: a non-nil roots pool
// overrides the platform trust store with roots, for injecting a test CA
// (e.g. from httptest.NewTLSServer) — it exists ONLY for tests in this
// package. Production code always goes through NewDoHResolver, which passes
// roots=nil and gets tls.Config's default (the system pool).
func newDoHResolver(rawURL string, roots *x509.CertPool) (*DoHResolver, error) {
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

	return &DoHResolver{
		url: rawURL,
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
		return nil, 0, false, &NoDataError{URL: d.url, Host: host}
	}

	return addrs, minTTL, haveTTL, nil
}

// queryFamily runs a single A or AAAA query over DoH.
func (d *DoHResolver) queryFamily(ctx context.Context, host string, qtype dnsmessage.Type) ([]netip.Addr, time.Duration, bool, error) {
	query, err := buildDoHQuery(host, qtype)
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
			return nil, 0, false, &TimeoutError{URL: d.url, Err: err}
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
			return nil, 0, false, &TimeoutError{URL: d.url, Err: err}
		}
		return nil, 0, false, &MalformedResponseError{URL: d.url, Err: err}
	}
	if len(body) > dohMaxMessageSize {
		return nil, 0, false, &MalformedResponseError{
			URL: d.url,
			Err: fmt.Errorf("response body exceeds max DNS message size of %d bytes", dohMaxMessageSize),
		}
	}

	return parseDoHResponse(d.url, body, host, qtype)
}

// buildDoHQuery packs a single-question A/AAAA query. Per RFC 8484 SS4.1, it
// uses a DNS ID of 0 to maximize HTTP cache friendliness.
func buildDoHQuery(host string, qtype dnsmessage.Type) ([]byte, error) {
	name, err := dnsmessage.NewName(fqdn(host))
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:               0,
			RecursionDesired: true,
		},
		Questions: []dnsmessage.Question{
			{Name: name, Type: qtype, Class: dnsmessage.ClassINET},
		},
	}
	return msg.Pack()
}

// parseDoHResponse unpacks body and extracts the addrs and minimum TTL of
// the qtype records it contains.
func parseDoHResponse(dohURL string, body []byte, host string, qtype dnsmessage.Type) ([]netip.Addr, time.Duration, bool, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return nil, 0, false, &MalformedResponseError{URL: dohURL, Err: err}
	}

	if msg.RCode == dnsmessage.RCodeNameError {
		return nil, 0, false, &NXDomainError{URL: dohURL, Host: host}
	}
	if msg.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, false, &MalformedResponseError{URL: dohURL, Err: fmt.Errorf("response RCode %v", msg.RCode)}
	}

	var addrs []netip.Addr
	var minTTL time.Duration
	haveTTL := false
	for _, ans := range msg.Answers {
		if ans.Header.Class != dnsmessage.ClassINET {
			continue
		}

		var addr netip.Addr
		switch res := ans.Body.(type) {
		case *dnsmessage.AResource:
			if qtype != dnsmessage.TypeA {
				continue
			}
			addr = netip.AddrFrom4(res.A)
		case *dnsmessage.AAAAResource:
			if qtype != dnsmessage.TypeAAAA {
				continue
			}
			addr = netip.AddrFrom16(res.AAAA)
		default:
			continue
		}

		addrs = append(addrs, addr)
		ttl := time.Duration(ans.Header.TTL) * time.Second
		if !haveTTL || ttl < minTTL {
			minTTL = ttl
			haveTTL = true
		}
	}

	return addrs, minTTL, haveTTL, nil
}

// fqdn returns host with a trailing dot, as dnsmessage.Name requires.
func fqdn(host string) string {
	if strings.HasSuffix(host, ".") {
		return host
	}
	return host + "."
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

// MalformedResponseError reports a DoH response body that failed to decode
// as a valid DNS message, or that decoded to an unexpected RCode.
type MalformedResponseError struct {
	URL string
	Err error
}

func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("dnsresolve: DoH %s returned a malformed response: %v", e.URL, e.Err)
}

func (e *MalformedResponseError) Unwrap() error { return e.Err }

// TimeoutError reports a DoH request that did not complete within its
// deadline.
type TimeoutError struct {
	URL string
	Err error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("dnsresolve: DoH %s request timed out: %v", e.URL, e.Err)
}

func (e *TimeoutError) Unwrap() error { return e.Err }

// NXDomainError reports a DoH provider answering NXDOMAIN for one address
// family. Lookup tolerates this as long as the other family answers.
type NXDomainError struct {
	URL  string
	Host string
}

func (e *NXDomainError) Error() string {
	return fmt.Sprintf("dnsresolve: DoH %s: no such host %q", e.URL, e.Host)
}

// NoDataError reports a DoH provider answering NOERROR with an empty final
// A+AAAA addr set for host (NODATA, or a CNAME chain with no A/AAAA
// target). It is the DoH-transport counterpart of the no-such-host error
// net.Resolver.LookupNetIP returns in the same situation.
type NoDataError struct {
	URL  string
	Host string
}

func (e *NoDataError) Error() string {
	return fmt.Sprintf("dnsresolve: DoH %s: no A/AAAA records for %q", e.URL, e.Host)
}
