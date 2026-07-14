package dnsresolve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// newCertPoolFromServer returns a CertPool trusting only srv's leaf
// certificate, mimicking how a caller injects a private/test CA via the
// unexported constructor seam.
func newCertPoolFromServer(t *testing.T, srv *httptest.Server) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

// newDoHTestResolver builds a DoHResolver that trusts srv's certificate via
// the unexported test-only constructor seam (no InsecureSkipVerify).
func newDoHTestResolver(t *testing.T, srv *httptest.Server, path string) *DoHResolver {
	t.Helper()
	pool := newCertPoolFromServer(t, srv)
	r, err := newDoHResolver(srv.URL+path, pool, "")
	if err != nil {
		t.Fatalf("newDoHResolver: unexpected error: %v", err)
	}
	return r
}

func readDoHQuestion(t *testing.T, r *http.Request) (dnsmessage.Message, dnsmessage.Type) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("DoH request method = %s, want POST", r.Method)
	}
	if ct := r.Header.Get("Content-Type"); ct != dohMediaType {
		t.Fatalf("DoH request Content-Type = %q, want %q", ct, dohMediaType)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading DoH request body: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		t.Fatalf("unpacking DoH request body: %v", err)
	}
	if len(msg.Questions) != 1 {
		t.Fatalf("DoH request has %d questions, want 1", len(msg.Questions))
	}
	return msg, msg.Questions[0].Type
}

func writeDoHAnswer(t *testing.T, w http.ResponseWriter, q dnsmessage.Question, answers []dnsmessage.Resource) {
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
		t.Fatalf("packing DoH response: %v", err)
	}
	w.Header().Set("Content-Type", dohMediaType)
	if _, err := w.Write(buf); err != nil {
		t.Fatalf("writing DoH response: %v", err)
	}
}

func aResource(t *testing.T, q dnsmessage.Question, ip [4]byte, ttl uint32) dnsmessage.Resource {
	t.Helper()
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl},
		Body:   &dnsmessage.AResource{A: ip},
	}
}

func aaaaResource(t *testing.T, q dnsmessage.Question, ip [16]byte, ttl uint32) dnsmessage.Resource {
	t.Helper()
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: ttl},
		Body:   &dnsmessage.AAAAResource{AAAA: ip},
	}
}

func TestDoHResolverLookupReturnsExpectedAddrsAndMinTTL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg, qtype := readDoHQuestion(t, r)
		q := msg.Questions[0]
		switch qtype {
		case dnsmessage.TypeA:
			writeDoHAnswer(t, w, q, []dnsmessage.Resource{
				aResource(t, q, [4]byte{192, 0, 2, 1}, 300),
				aResource(t, q, [4]byte{192, 0, 2, 2}, 120),
			})
		case dnsmessage.TypeAAAA:
			writeDoHAnswer(t, w, q, []dnsmessage.Resource{
				aaaaResource(t, q, [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, 60),
			})
		default:
			t.Fatalf("unexpected query type %v", qtype)
		}
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

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
		netip.MustParseAddr("192.0.2.1"):   true,
		netip.MustParseAddr("192.0.2.2"):   true,
		netip.MustParseAddr("2001:db8::1"): true,
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

func TestDoHResolverToleratesFamilyNXDOMAIN(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg, qtype := readDoHQuestion(t, r)
		q := msg.Questions[0]
		switch qtype {
		case dnsmessage.TypeA:
			writeDoHAnswer(t, w, q, []dnsmessage.Resource{
				aResource(t, q, [4]byte{192, 0, 2, 9}, 30),
			})
		case dnsmessage.TypeAAAA:
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{Response: true, RCode: dnsmessage.RCodeNameError},
				Questions: []dnsmessage.Question{q},
			}
			buf, err := resp.Pack()
			if err != nil {
				t.Fatalf("packing NXDOMAIN response: %v", err)
			}
			w.Header().Set("Content-Type", dohMediaType)
			_, _ = w.Write(buf)
		default:
			t.Fatalf("unexpected query type %v", qtype)
		}
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, _, _, err := r.Lookup(ctx, "v4only.example.com")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != netip.MustParseAddr("192.0.2.9") {
		t.Fatalf("Lookup: got %v, want [192.0.2.9]", addrs)
	}
}

// TestDoHResolverDoubleNODATAIsError covers the case where both families
// answer NOERROR with zero records (NODATA, or a CNAME chain with no
// A/AAAA target): Lookup must return a typed error rather than ([], nil),
// matching SystemResolver's no-such-host behavior for the same situation.
func TestDoHResolverDoubleNODATAIsError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg, _ := readDoHQuestion(t, r)
		q := msg.Questions[0]
		writeDoHAnswer(t, w, q, nil) // NOERROR, zero answers
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

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

func TestDoHResolverMalformedResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readDoHQuestion(t, r)
		w.Header().Set("Content-Type", dohMediaType)
		_, _ = w.Write([]byte{0x01, 0x02, 0x03}) // not a valid DNS message
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, _, err := r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected error for malformed response, got nil")
	}
	var malformed *MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("Lookup: err = %v (%T), want *MalformedResponseError", err, err)
	}
}

// TestDoHResolverOversizedResponseIsError covers the io.LimitReader cap: a
// body larger than the maximum possible DNS message size must fail typed
// rather than being buffered in full.
func TestDoHResolverOversizedResponseIsError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readDoHQuestion(t, r)
		w.Header().Set("Content-Type", dohMediaType)
		_, _ = w.Write(make([]byte, dohMaxMessageSize+1))
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, _, err := r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected error for oversized response, got nil")
	}
	var malformed *MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("Lookup: err = %v (%T), want *MalformedResponseError", err, err)
	}
}

func TestDoHResolverNon200Status(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readDoHQuestion(t, r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newDoHTestResolver(t, srv, "/dns-query")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, _, err := r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected error for non-200 status, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Lookup: err = %v (%T), want *StatusError", err, err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusError.StatusCode = %d, want %d", statusErr.StatusCode, http.StatusInternalServerError)
	}
}

func TestDoHResolverTimeout(t *testing.T) {
	blockCh := make(chan struct{})

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readDoHQuestion(t, r)
		<-blockCh // never respond within the test's short context deadline
	}))
	// srv.Close() blocks until the handler goroutine returns, so blockCh
	// must be closed BEFORE srv.Close() runs, not after (a bare
	// `defer close(blockCh)` registered before `defer srv.Close()` would
	// run second, i.e. too late, and deadlock the test).
	defer func() {
		close(blockCh)
		srv.Close()
	}()

	r := newDoHTestResolver(t, srv, "/dns-query")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, _, err := r.Lookup(ctx, "example.com")
	if err == nil {
		t.Fatal("Lookup: expected timeout error, got nil")
	}
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Lookup: err = %v (%T), want *TimeoutError", err, err)
	}
}

func TestNewDoHResolverRejectsNonHTTPS(t *testing.T) {
	if _, err := NewDoHResolver("http://example.com/dns-query"); err == nil {
		t.Fatal("NewDoHResolver(http://...): expected error, got nil")
	}
}

// TestNewDoHResolverWithBootstrapDialsBootstrapIP is the hermetic construct
// test for the BOOTSTRAP-IP invariant (Q33): rawURL names a HOSTNAME that
// does not exist in DNS, so if the resolver dialed it via the system
// resolver the lookup itself would fail before any TLS handshake happens.
// Using the unexported constructor seam (to inject the hermetic listener's
// self-signed test CA — NewDoHResolverWithBootstrap, like NewDoHResolver,
// always uses the platform trust store), it instead dials bootstrapIP,
// proving the DialContext override — not the URL host — determines the TCP
// connect target, while the listener's certificate is issued for the URL's
// original hostname to also confirm the TLS ServerName / SNI is untouched.
func TestNewDoHResolverWithBootstrapDialsBootstrapIP(t *testing.T) {
	const unresolvableHost = "doh-resolver.invalid.wanbond-test"

	cert, pool := generateDoTTestCert(t, unresolvableHost)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg, qtype := readDoHQuestion(t, r)
		q := msg.Questions[0]
		if qtype == dnsmessage.TypeA {
			writeDoHAnswer(t, w, q, []dnsmessage.Resource{aResource(t, q, [4]byte{192, 0, 2, 42}, 30)})
			return
		}
		resp := dnsmessage.Message{Header: dnsmessage.Header{Response: true, RCode: dnsmessage.RCodeNameError}, Questions: []dnsmessage.Question{q}}
		buf, err := resp.Pack()
		if err != nil {
			t.Fatalf("packing NXDOMAIN response: %v", err)
		}
		w.Header().Set("Content-Type", dohMediaType)
		_, _ = w.Write(buf)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	bootstrapIP, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting listener addr: %v", err)
	}
	// Rewrite the server's self-reported URL to use the unresolvable hostname
	// instead of its loopback IP, so the resolver sees a hostname-form URL
	// exactly like the config.DNS.NewResolver caller does.
	rawURL := "https://" + net.JoinHostPort(unresolvableHost, port) + "/dns-query"

	r, err := newDoHResolver(rawURL, pool, bootstrapIP)
	if err != nil {
		t.Fatalf("newDoHResolver: unexpected error: %v", err)
	}
	if r.DialHost() != bootstrapIP {
		t.Fatalf("DialHost() = %q, want %q", r.DialHost(), bootstrapIP)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, _, _, err := r.Lookup(ctx, "example.com")
	if err != nil {
		t.Fatalf("Lookup: unexpected error (bootstrap dial should have reached the hermetic listener without any DNS lookup of %q): %v", unresolvableHost, err)
	}
	if len(addrs) != 1 || addrs[0] != netip.MustParseAddr("192.0.2.42") {
		t.Fatalf("Lookup: got %v, want [192.0.2.42]", addrs)
	}
}

// TestNewDoHResolverWithBootstrapStoresDialTarget is the plain constructor
// test (no live dial) for the exported NewDoHResolverWithBootstrap: it must
// record bootstrapIP as the DialHost every subsequent Lookup pins its TCP
// connect address to.
func TestNewDoHResolverWithBootstrapStoresDialTarget(t *testing.T) {
	r, err := NewDoHResolverWithBootstrap("https://resolver.example.com/dns-query", "198.51.100.1")
	if err != nil {
		t.Fatalf("NewDoHResolverWithBootstrap: unexpected error: %v", err)
	}
	if r.DialHost() != "198.51.100.1" {
		t.Fatalf("DialHost() = %q, want %q", r.DialHost(), "198.51.100.1")
	}
}
