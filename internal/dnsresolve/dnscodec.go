package dnsresolve

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// buildQuery packs a single-question A/AAAA query with a DNS ID of 0, shared
// by both the DoH (RFC 8484) and DoT (RFC 7858) transports. Per RFC 8484
// SS4.1 the zero ID maximizes HTTP cache friendliness for DoH; for DoT it is
// simply harmless, since DoT queries aren't cached by an intermediary.
func buildQuery(host string, qtype dnsmessage.Type) ([]byte, error) {
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

// parseAnswer unpacks body as a DNS response to a qtype query for host, and
// extracts the qtype-matching addrs and their minimum TTL. endpoint
// identifies the resolver for error messages (a DoH URL or a DoT
// server:port) — shared by both transports.
func parseAnswer(endpoint string, body []byte, host string, qtype dnsmessage.Type) ([]netip.Addr, time.Duration, bool, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return nil, 0, false, &MalformedResponseError{Endpoint: endpoint, Err: err}
	}

	if msg.RCode == dnsmessage.RCodeNameError {
		return nil, 0, false, &NXDomainError{Endpoint: endpoint, Host: host}
	}
	if msg.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, false, &MalformedResponseError{Endpoint: endpoint, Err: fmt.Errorf("response RCode %v", msg.RCode)}
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

// MalformedResponseError reports a resolver response body that failed to
// decode as a valid DNS message, or that decoded to an unexpected RCode.
// Shared by the DoH and DoT transports.
type MalformedResponseError struct {
	Endpoint string
	Err      error
}

func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("dnsresolve: %s returned a malformed response: %v", e.Endpoint, e.Err)
}

func (e *MalformedResponseError) Unwrap() error { return e.Err }

// TimeoutError reports a resolver request that did not complete within its
// deadline. Shared by the DoH and DoT transports.
type TimeoutError struct {
	Endpoint string
	Err      error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("dnsresolve: %s request timed out: %v", e.Endpoint, e.Err)
}

func (e *TimeoutError) Unwrap() error { return e.Err }

// NXDomainError reports a resolver answering NXDOMAIN for one address
// family. Lookup tolerates this as long as the other family answers. Shared
// by the DoH and DoT transports.
type NXDomainError struct {
	Endpoint string
	Host     string
}

func (e *NXDomainError) Error() string {
	return fmt.Sprintf("dnsresolve: %s: no such host %q", e.Endpoint, e.Host)
}

// NoDataError reports a resolver answering NOERROR with an empty final
// A+AAAA addr set for host (NODATA, or a CNAME chain with no A/AAAA
// target). It is the DoH/DoT-transport counterpart of the no-such-host error
// net.Resolver.LookupNetIP returns in the same situation. Shared by the DoH
// and DoT transports.
type NoDataError struct {
	Endpoint string
	Host     string
}

func (e *NoDataError) Error() string {
	return fmt.Sprintf("dnsresolve: %s: no A/AAAA records for %q", e.Endpoint, e.Host)
}
