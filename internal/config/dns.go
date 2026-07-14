package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/7mind/wanbond/internal/dnsresolve"
)

// DNSResolverMode selects the transport dns.resolver picks.
type DNSResolverMode string

const (
	// DNSResolverSystem uses the OS stub resolver (dnsresolve.SystemResolver).
	// It is the default when [dns] is omitted or resolver is left empty.
	DNSResolverSystem DNSResolverMode = "system"
	// DNSResolverDoH uses DNS-over-HTTPS (dnsresolve.DoHResolver).
	DNSResolverDoH DNSResolverMode = "doh"
	// DNSResolverDoT uses DNS-over-TLS (dnsresolve.DoTResolver).
	DNSResolverDoT DNSResolverMode = "dot"
)

func (m DNSResolverMode) valid() bool {
	return m == DNSResolverSystem || m == DNSResolverDoH || m == DNSResolverDoT
}

// Default cadence/timeout for the [dns] block (Q31): the poll interval sits on
// the reconcile-cadence scale — frequent enough that a DDNS repoint is picked
// up promptly, far coarser than the sub-second deferred-path/probe loops
// (bind.DefaultReconcileInterval = 1s) since re-resolution costs a real
// network round trip to the configured resolver and re-resolving too eagerly
// would needlessly load it. The per-lookup timeout mirrors dnsresolve's own
// internal DoH/DoT bounds (5s).
const (
	defaultDNSPollInterval = 30 * time.Second
	defaultDNSTimeout      = 5 * time.Second
)

// dotDefaultPort mirrors dnsresolve's unexported dotPort: DNS-over-TLS's
// IANA-assigned port (RFC 7858 §3), and the ONLY port dnsresolve.NewDoTResolver
// ever dials (it appends this fixed port itself). An explicit port in
// dot_server must match it, so a mistyped non-standard port fails fast at
// config load rather than being silently ignored when the resolver is built.
const dotDefaultPort = "853"

// DNS selects and tunes the resolver transport used for opted-in hostname
// peer-endpoint re-resolution (Q29/Q31). An ABSENT [dns] block is INERT: it
// defaults to the system resolver, and resolution only happens at all when a
// peer separately opts in with its own `dns = true` flag (Peer.DNS) —
// [dns] merely selects which transport that opt-in uses, it is never a gate
// by itself.
type DNS struct {
	// Resolver selects the transport: "system" (default), "doh", or "dot".
	Resolver DNSResolverMode `toml:"resolver"`
	// DoHURL is the DNS-over-HTTPS query endpoint (an https:// URL). Required
	// iff resolver = "doh"; rejected (must stay empty) under any other
	// resolver.
	DoHURL string `toml:"doh_url"`
	// DoTServer is the DNS-over-TLS server: a bare host, or "host:port" where
	// port must be dotDefaultPort (853) — dnsresolve.NewDoTResolver always
	// dials that port itself, so any other explicit port cannot be honored.
	// Required iff resolver = "dot"; rejected (must stay empty) under any
	// other resolver.
	DoTServer string `toml:"dot_server"`
	// BootstrapIP is the literal IP address wanbond dials the DoH/DoT server
	// on when doh_url/dot_server names it by HOSTNAME rather than IP literal
	// (the chicken-and-egg BOOTSTRAP-IP invariant, Q33): resolving the
	// private resolver's own name would itself require a DNS lookup — via
	// the system resolver [dns] exists to avoid — defeating the point of
	// configuring a private resolver. Required iff the doh_url/dot_server
	// host is a hostname; ignored when it is already an IP literal.
	BootstrapIP string `toml:"bootstrap_ip"`
	// PollInterval is the re-resolution cadence for an opted-in hostname peer
	// endpoint (Q31), parsed from PollIntervalRaw in applyDefaults. Defaults to
	// defaultDNSPollInterval when PollIntervalRaw is left empty; must be > 0
	// after defaulting.
	PollInterval time.Duration `toml:"-"`
	// PollIntervalRaw is the TOML Go-duration string form of PollInterval,
	// e.g. "30s". Mirrors Path.LinkRTTRaw: go-toml/v2 cannot decode a TOML
	// string directly into a time.Duration field, so the raw string is parsed
	// via time.ParseDuration in applyDefaults.
	PollIntervalRaw string `toml:"poll_interval"`
	// Timeout bounds a single resolver lookup, parsed from TimeoutRaw in
	// applyDefaults. Defaults to defaultDNSTimeout when TimeoutRaw is left
	// empty; must be > 0 after defaulting.
	Timeout time.Duration `toml:"-"`
	// TimeoutRaw is the TOML Go-duration string form of Timeout, e.g. "5s".
	TimeoutRaw string `toml:"timeout"`
}

// applyDefaults fills the resolver mode and cadence/timeout knobs left at
// their zero value, so a minimal or absent [dns] block resolves to explicit,
// usable settings — an absent block still ends up as the system resolver
// with the standard cadence/timeout, per the type's zero-value contract.
// PollIntervalRaw/TimeoutRaw are parsed here (mirroring Path's *Raw-field
// normalize step for LinkRTTRaw); an unparseable duration string is reported
// immediately.
func (d *DNS) applyDefaults() error {
	if d.Resolver == "" {
		d.Resolver = DNSResolverSystem
	}
	if d.PollIntervalRaw == "" {
		d.PollInterval = defaultDNSPollInterval
	} else {
		v, err := time.ParseDuration(d.PollIntervalRaw)
		if err != nil {
			return fmt.Errorf("dns.poll_interval: invalid duration %q: %w", d.PollIntervalRaw, err)
		}
		d.PollInterval = v
	}
	if d.TimeoutRaw == "" {
		d.Timeout = defaultDNSTimeout
	} else {
		v, err := time.ParseDuration(d.TimeoutRaw)
		if err != nil {
			return fmt.Errorf("dns.timeout: invalid duration %q: %w", d.TimeoutRaw, err)
		}
		d.Timeout = v
	}
	return nil
}

// validate enforces the [dns] block invariants: a resolver-mode-appropriate
// set of required fields, no stray fields left over from a different mode,
// positive cadence/timeout, and the BOOTSTRAP-IP invariant for a hostname-form
// doh_url/dot_server. Runs after applyDefaults (mirroring Amnezia/FEC/
// SchedulerConfig), so Resolver/PollInterval/Timeout are already defaulted.
func (d DNS) validate() error {
	if !d.Resolver.valid() {
		return fmt.Errorf("dns.resolver must be %q, %q or %q, got %q", DNSResolverSystem, DNSResolverDoH, DNSResolverDoT, d.Resolver)
	}
	if d.PollInterval <= 0 {
		return fmt.Errorf("dns.poll_interval must be > 0, got %s", d.PollInterval)
	}
	if d.Timeout <= 0 {
		return fmt.Errorf("dns.timeout must be > 0, got %s", d.Timeout)
	}

	switch d.Resolver {
	case DNSResolverSystem:
		if d.DoHURL != "" {
			return errors.New("dns.doh_url is only meaningful when dns.resolver = \"doh\"")
		}
		if d.DoTServer != "" {
			return errors.New("dns.dot_server is only meaningful when dns.resolver = \"dot\"")
		}
		if d.BootstrapIP != "" {
			return errors.New("dns.bootstrap_ip is only meaningful when dns.resolver is \"doh\" or \"dot\"")
		}
		return nil
	case DNSResolverDoH:
		if d.DoTServer != "" {
			return errors.New("dns.dot_server is not meaningful when dns.resolver = \"doh\"")
		}
		if d.DoHURL == "" {
			return errors.New("dns.doh_url is required when dns.resolver = \"doh\"")
		}
		host, err := dohURLHost(d.DoHURL)
		if err != nil {
			return fmt.Errorf("dns.doh_url: %w", err)
		}
		return d.requireBootstrapForHost(host)
	case DNSResolverDoT:
		if d.DoHURL != "" {
			return errors.New("dns.doh_url is not meaningful when dns.resolver = \"dot\"")
		}
		if d.DoTServer == "" {
			return errors.New("dns.dot_server is required when dns.resolver = \"dot\"")
		}
		host, err := dotServerHost(d.DoTServer)
		if err != nil {
			return fmt.Errorf("dns.dot_server: %w", err)
		}
		return d.requireBootstrapForHost(host)
	default:
		// Unreachable: d.Resolver.valid() above already rejected anything else.
		return fmt.Errorf("dns.resolver: unhandled mode %q", d.Resolver)
	}
}

// requireBootstrapForHost enforces the BOOTSTRAP-IP invariant for the given
// (already-extracted) doh_url/dot_server host: an IP literal needs no
// bootstrap; a hostname requires an explicit, valid bootstrap_ip.
func (d DNS) requireBootstrapForHost(host string) error {
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if d.BootstrapIP == "" {
		return fmt.Errorf("hostname %q requires dns.bootstrap_ip: the resolver's own address must be reachable without a DNS lookup (a plaintext lookup of the private resolver's own name would defeat the point of configuring one)", host)
	}
	if _, err := netip.ParseAddr(d.BootstrapIP); err != nil {
		return fmt.Errorf("dns.bootstrap_ip %q is not a valid IP literal: %w", d.BootstrapIP, err)
	}
	return nil
}

// dohURLHost extracts and validates the host component of a doh_url: it must
// be a well-formed https:// URL with a non-empty host.
func dohURLHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("must use https, got scheme %q in %q", u.Scheme, rawURL)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host in %q", rawURL)
	}
	return host, nil
}

// dotServerHost extracts and validates the host component of a dot_server: a
// bare host (hostname or IP literal, no port) is accepted as-is; a
// "host:port" form is split and requires port == dotDefaultPort, since
// dnsresolve.NewDoTResolver always dials that fixed port itself.
func dotServerHost(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty")
	}
	// A bare IP literal (including an unbracketed IPv6 literal, which itself
	// contains colons) has no port to split off.
	if _, err := netip.ParseAddr(raw); err == nil {
		return raw, nil
	}
	if !strings.Contains(raw, ":") {
		return raw, nil // bare hostname, no port
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid dot_server %q: %w", raw, err)
	}
	if port != dotDefaultPort {
		return "", fmt.Errorf("port %q must be %q (the IANA-assigned DNS-over-TLS port; dnsresolve always dials it)", port, dotDefaultPort)
	}
	return host, nil
}

// NewResolver constructs the dnsresolve.Resolver implementation the
// (validated) [dns] block selects: dnsresolve.NewSystemResolver for the
// default/system mode, dnsresolve.NewDoHResolver/NewDoTResolver for doh/dot,
// or their *WithBootstrap variants when bootstrap_ip is set. Callers
// normally obtain a DNS value via config.Load, which already ran
// applyDefaults/validate; calling this on an un-validated DNS value surfaces
// whatever the underlying dnsresolve constructor rejects.
//
// bootstrap_ip (validated fail-fast, see requireBootstrapForHost) IS wired
// into the constructed resolver's dial target: when set, the resolver dials
// bootstrap_ip directly instead of resolving the configured hostname through
// the system dialer — the BOOTSTRAP-IP invariant (Q33) exists precisely to
// avoid that plaintext lookup of the private resolver's own name.
func (d DNS) NewResolver() (dnsresolve.Resolver, error) {
	switch d.Resolver {
	case DNSResolverDoH:
		if d.BootstrapIP != "" {
			return dnsresolve.NewDoHResolverWithBootstrap(d.DoHURL, d.BootstrapIP)
		}
		return dnsresolve.NewDoHResolver(d.DoHURL)
	case DNSResolverDoT:
		host, err := dotServerHost(d.DoTServer)
		if err != nil {
			return nil, fmt.Errorf("dns.dot_server: %w", err)
		}
		if d.BootstrapIP != "" {
			return dnsresolve.NewDoTResolverWithBootstrap(host, d.BootstrapIP)
		}
		return dnsresolve.NewDoTResolver(host)
	default:
		return dnsresolve.NewSystemResolver(), nil
	}
}
