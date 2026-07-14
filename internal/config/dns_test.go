package config

import (
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/dnsresolve"
)

// TestDNSAbsentBlockYieldsSystemDefaults: an omitted [dns] block (the fixture
// configs carry none) must default to the system resolver with the standard
// cadence/timeout — never a zero-value, inert-looking block.
func TestDNSAbsentBlockYieldsSystemDefaults(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DNS.Resolver != DNSResolverSystem {
		t.Fatalf("dns.resolver = %q, want %q", c.DNS.Resolver, DNSResolverSystem)
	}
	if c.DNS.PollInterval != defaultDNSPollInterval {
		t.Fatalf("dns.poll_interval = %s, want default %s", c.DNS.PollInterval, defaultDNSPollInterval)
	}
	if c.DNS.Timeout != defaultDNSTimeout {
		t.Fatalf("dns.timeout = %s, want default %s", c.DNS.Timeout, defaultDNSTimeout)
	}
	r, err := c.DNS.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if _, ok := r.(*dnsresolve.SystemResolver); !ok {
		t.Fatalf("NewResolver() = %T, want *dnsresolve.SystemResolver", r)
	}
}

// TestDNSValidateRejects is the [dns]-block validation matrix (acceptance):
// doh without doh_url, dot without dot_server, a hostname-form doh_url/
// dot_server without bootstrap_ip, and poll_interval <= 0 must all fail fast
// with a clear error.
func TestDNSValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "doh without doh_url",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"doh\"\n",
			want: "dns.doh_url is required",
		},
		{
			name: "dot without dot_server",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"dot\"\n",
			want: "dns.dot_server is required",
		},
		{
			name: "doh hostname url without bootstrap_ip",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"doh\"\ndoh_url = \"https://resolver.example.com/dns-query\"\n",
			want: "requires dns.bootstrap_ip",
		},
		{
			name: "dot hostname server without bootstrap_ip",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"dot\"\ndot_server = \"resolver.example.com\"\n",
			want: "requires dns.bootstrap_ip",
		},
		{
			name: "poll_interval <= 0",
			body: fill(edgeConfig) + "\n[dns]\npoll_interval = \"-1s\"\n",
			want: "dns.poll_interval must be > 0",
		},
		{
			name: "timeout <= 0",
			body: fill(edgeConfig) + "\n[dns]\ntimeout = \"-1s\"\n",
			want: "dns.timeout must be > 0",
		},
		{
			name: "poll_interval unparseable duration",
			body: fill(edgeConfig) + "\n[dns]\npoll_interval = \"not-a-duration\"\n",
			want: "dns.poll_interval: invalid duration",
		},
		{
			name: "timeout unparseable duration",
			body: fill(edgeConfig) + "\n[dns]\ntimeout = \"not-a-duration\"\n",
			want: "dns.timeout: invalid duration",
		},
		{
			name: "unknown resolver mode",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"custom\"\n",
			want: "dns.resolver must be",
		},
		{
			name: "doh_url set under system resolver",
			body: fill(edgeConfig) + "\n[dns]\ndoh_url = \"https://198.51.100.1/dns-query\"\n",
			want: "dns.doh_url is only meaningful",
		},
		{
			name: "dot_server set under system resolver",
			body: fill(edgeConfig) + "\n[dns]\ndot_server = \"198.51.100.1\"\n",
			want: "dns.dot_server is only meaningful",
		},
		{
			name: "bootstrap_ip set under system resolver",
			body: fill(edgeConfig) + "\n[dns]\nbootstrap_ip = \"198.51.100.1\"\n",
			want: "dns.bootstrap_ip is only meaningful",
		},
		{
			name: "dot_server set under doh resolver",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"doh\"\ndoh_url = \"https://198.51.100.1/dns-query\"\ndot_server = \"198.51.100.1\"\n",
			want: "dns.dot_server is not meaningful",
		},
		{
			name: "doh_url set under dot resolver",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"dot\"\ndot_server = \"198.51.100.1\"\ndoh_url = \"https://198.51.100.1/dns-query\"\n",
			want: "dns.doh_url is not meaningful",
		},
		{
			name: "doh_url non-https scheme",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"doh\"\ndoh_url = \"http://198.51.100.1/dns-query\"\n",
			want: "must use https",
		},
		{
			name: "dot_server non-standard port",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"dot\"\ndot_server = \"198.51.100.1:8853\"\n",
			want: "must be \"853\"",
		},
		{
			name: "bootstrap_ip not a valid IP literal",
			body: fill(edgeConfig) + "\n[dns]\nresolver = \"dot\"\ndot_server = \"resolver.example.com\"\nbootstrap_ip = \"not-an-ip\"\n",
			want: "is not a valid IP literal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestDNSFullDoHBlockConstructsResolver: a full, valid doh block loads and
// NewResolver constructs the matching dnsresolve.DoHResolver.
func TestDNSFullDoHBlockConstructsResolver(t *testing.T) {
	body := fill(edgeConfig) + "\n[dns]\n" +
		"resolver = \"doh\"\n" +
		"doh_url = \"https://198.51.100.1/dns-query\"\n" +
		"poll_interval = \"60s\"\n" +
		"timeout = \"3s\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DNS.Resolver != DNSResolverDoH {
		t.Fatalf("dns.resolver = %q, want %q", c.DNS.Resolver, DNSResolverDoH)
	}
	r, err := c.DNS.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if _, ok := r.(*dnsresolve.DoHResolver); !ok {
		t.Fatalf("NewResolver() = %T, want *dnsresolve.DoHResolver", r)
	}
}

// TestDNSFullDoTBlockConstructsResolver: a full, valid dot block (IP-literal
// server, so no bootstrap_ip is needed) loads and NewResolver constructs the
// matching dnsresolve.DoTResolver.
func TestDNSFullDoTBlockConstructsResolver(t *testing.T) {
	body := fill(edgeConfig) + "\n[dns]\n" +
		"resolver = \"dot\"\n" +
		"dot_server = \"198.51.100.1\"\n" +
		"poll_interval = \"60s\"\n" +
		"timeout = \"3s\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DNS.Resolver != DNSResolverDoT {
		t.Fatalf("dns.resolver = %q, want %q", c.DNS.Resolver, DNSResolverDoT)
	}
	r, err := c.DNS.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if _, ok := r.(*dnsresolve.DoTResolver); !ok {
		t.Fatalf("NewResolver() = %T, want *dnsresolve.DoTResolver", r)
	}
}

// TestDNSFullDoTBlockHostnameWithBootstrapIP: a hostname-form dot_server WITH
// bootstrap_ip passes validation and constructs a DoTResolver that dials
// bootstrap_ip:853 — never the hostname (the BOOTSTRAP-IP invariant, Q33):
// resolving the private resolver's own name via the system dialer would leak
// that lookup in plaintext.
func TestDNSFullDoTBlockHostnameWithBootstrapIP(t *testing.T) {
	body := fill(edgeConfig) + "\n[dns]\n" +
		"resolver = \"dot\"\n" +
		"dot_server = \"resolver.example.com:853\"\n" +
		"bootstrap_ip = \"198.51.100.1\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := c.DNS.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	dot, ok := r.(*dnsresolve.DoTResolver)
	if !ok {
		t.Fatalf("NewResolver() = %T, want *dnsresolve.DoTResolver", r)
	}
	if want := "198.51.100.1:853"; dot.DialAddr() != want {
		t.Fatalf("DialAddr() = %q, want %q (must dial the bootstrap IP, not the hostname)", dot.DialAddr(), want)
	}
}

// TestDNSFullDoHBlockHostnameWithBootstrapIP mirrors
// TestDNSFullDoTBlockHostnameWithBootstrapIP for DoH: a hostname-form
// doh_url WITH bootstrap_ip passes validation and constructs a DoHResolver
// that pins its TCP connect address to bootstrap_ip — never the hostname.
func TestDNSFullDoHBlockHostnameWithBootstrapIP(t *testing.T) {
	body := fill(edgeConfig) + "\n[dns]\n" +
		"resolver = \"doh\"\n" +
		"doh_url = \"https://resolver.example.com/dns-query\"\n" +
		"bootstrap_ip = \"198.51.100.1\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r, err := c.DNS.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	doh, ok := r.(*dnsresolve.DoHResolver)
	if !ok {
		t.Fatalf("NewResolver() = %T, want *dnsresolve.DoHResolver", r)
	}
	if want := "198.51.100.1"; doh.DialHost() != want {
		t.Fatalf("DialHost() = %q, want %q (must dial the bootstrap IP, not the hostname)", doh.DialHost(), want)
	}
}

// TestDNSDocumentedDurationStringFormLoads is the acceptance test for the
// documented [dns] duration form (docs/install.md, wanbond.example.toml):
// poll_interval = "30s" / timeout = "5s" must load, not be rejected by
// go-toml/v2 (which cannot decode a TOML string directly into
// time.Duration — see PollIntervalRaw/TimeoutRaw).
func TestDNSDocumentedDurationStringFormLoads(t *testing.T) {
	body := fill(edgeConfig) + "\n[dns]\n" +
		"poll_interval = \"30s\"\n" +
		"timeout = \"5s\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DNS.PollInterval != 30*time.Second {
		t.Fatalf("dns.poll_interval = %s, want 30s", c.DNS.PollInterval)
	}
	if c.DNS.Timeout != 5*time.Second {
		t.Fatalf("dns.timeout = %s, want 5s", c.DNS.Timeout)
	}
}
