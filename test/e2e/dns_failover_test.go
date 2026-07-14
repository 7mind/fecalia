//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// TestDNSHubResolveAndReroute is the v1 DNS acceptance bar (Q36 / R70 / the D32 regression
// guard) exercised END TO END in the privileged netns sandbox. The edge names the
// concentrator by HOSTNAME (peer dns = true, [dns] resolver = "system"); a minimal in-test
// UDP DNS responder running INSIDE the edge network namespace is the ONLY answer source, so
// the whole scenario is hermetic — no packet ever leaves the sandbox for name resolution.
//
// Scenario (the four beats the task specifies):
//
//	(0) BOOT WHILE UNRESOLVABLE. The responder answers NXDOMAIN. The edge's bounded boot
//	    resolve fails, so the peer boots ENDPOINT-LESS (tolerant boot): the daemon comes up,
//	    the TUN appears, but no WireGuard endpoint is installed and the tunnel does not
//	    handshake. Proven by the "boots deferred" boot-resolve log marker AND a bounded
//	    negative reachability check (the tunnel must NOT come up while the name has no answer).
//
//	(1) FIRST SUCCESSFUL RESOLVE INSTALLS THE ENDPOINT (R70). The responder starts answering
//	    A -> concHubA. The re-resolution poll loop's first success adopts the head through the
//	    FIRST-RESOLVE INSTALL PATH: it installs the endpoint on the engine peer and
//	    re-handshakes. The tunnel comes up and traffic flows (ping + iperf3 > 0). Proven by the
//	    "first endpoint resolution" hub-failover log marker.
//
//	(2)+(3) CONCENTRATOR IP CHANGES MID-SESSION AND THE EDGE REPOINTS. The concentrator is
//	    multi-homed on concHubA and concHubB (two bound paths on the same peer veth — a
//	    renumbering that adds the new address, cuts DNS over, then decommissions the old). We
//	    flip the DNS answer to concHubB and FLUSH concHubA from the concentrator namespace, so
//	    the old address is genuinely gone. The edge re-resolves — via the scheduled poll and,
//	    faster, the out-of-band liveness-loss trigger once every path to concHubA reads DOWN —
//	    sees the active endpoint's IP change, repoints the bond's per-path remotes with exactly
//	    one SetPeerRemote, and re-handshakes. Proven by the "active concentrator endpoint
//	    re-resolved" hub-failover log marker.
//
//	(4) TUNNEL SURVIVES — TRAFFIC ACTUALLY RESUMES (D32 guard). Because concHubA is flushed,
//	    post-change traffic can flow ONLY if the edge is genuinely sending to concHubB. We
//	    assert reachability AND a positive iperf3 transfer AFTER the change: that is the D32
//	    regression guard operationalized end to end — a SetPeerRemote re-baselines the peer's
//	    receive resequencer so the standby hub's first (low outer-seq) frame re-anchors the
//	    release point instead of being dropped as a suspect low seq. A bare handshake completing
//	    is NOT enough; the resequencer must re-baseline for bytes to move, and moving bytes is
//	    what this asserts.
//
// Name resolution inside the netns needs a netns-LOCAL answer source: /etc/hosts and
// /etc/resolv.conf are filesystem-global (shared inode), not network-namespace scoped, so a
// plain edit would leak to the host and every other test. The TestMain re-exec already gives
// this process its own MOUNT namespace (unshare -m/-rm), so we bind-mount a private
// resolv.conf that points the Go resolver at the in-netns responder; the mount is invisible
// to the host and torn down on cleanup. The edge daemon is started with GODEBUG=netdns=go so
// the PURE-GO resolver (which reads that resolv.conf and speaks plain UDP to the responder) is
// used deterministically on both e2e hosts, rather than a cgo getaddrinfo path whose behaviour
// depends on the host's nsswitch configuration.
//
// Requires CAP_NET_ADMIN + a mount namespace + /dev/net/tun; the plain `go test` never
// compiles it (e2e build tag). Run as root on the e2e hosts:
//
//	go test -tags e2e -run DNS ./test/e2e
func TestDNSHubResolveAndReroute(t *testing.T) {
	bin := buildWanbond(t)

	// One veth pair. The concentrator is multi-homed: concHubA is assigned by Setup; concHubB
	// is added below. Both sit in the concentrator namespace on the same veth, in the edge
	// path's /24, so the single edge path reaches either.
	edgePath := pathSpec{
		name: "wan", edgeIP: "10.100.7.1", concIP: dnsConcHubA,
		edgeVeth: "wbGe", concVeth: "wbGc", delayMs: 20,
	}
	top := SetupWithPaths(t, []pathSpec{edgePath})

	// Bring the concentrator's SECOND public address up before it starts, so it binds both
	// hub addresses at boot (concHubA the initial answer, concHubB the post-renumber answer).
	concAddAddr(top, edgePath.concVeth, dnsConcHubB)

	// The in-netns DNS responder, bound to the edge veth address (guaranteed assigned; a fresh
	// netns loopback has no 127.0.0.1 until one is added). It starts UNRESOLVABLE (NXDOMAIN).
	resp := startDNSResponder(t, edgePath.edgeIP+":53", dnsHubHost)

	// Point the pure-Go resolver at the responder via a private (mount-namespace-local)
	// resolv.conf. ndots:0 makes the multi-label hostname a resolved-as-absolute query.
	mountResolvConf(t, edgePath.edgeIP)

	edgeCfg, concCfg := writeDNSConfigs(t, edgePath)

	// Concentrator first (must listen before the edge initiates), then the edge with the
	// pure-Go resolver forced on.
	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProcEnv(t, "edge", []string{"GODEBUG=netdns=go"}, bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")

	// -------- (0) endpoint-less tolerant boot while the name is unresolvable --------
	if !waitLogContains(edge, "boots deferred", 10*time.Second) {
		t.Fatalf("edge did not log the tolerant endpoint-less boot (initial resolve should have failed NXDOMAIN)\n--- edge ---\n%s", edge.log())
	}
	if top.pingUntil(concInner, 2*time.Second) {
		t.Fatalf("tunnel came up while the concentrator name was unresolvable; it must boot endpoint-less and stay down until the first successful resolve\n--- edge ---\n%s", edge.log())
	}
	t.Logf("(0) edge booted endpoint-less while %q was NXDOMAIN; tunnel correctly down", dnsHubHost)

	// -------- (1) first successful resolve installs the endpoint (R70) --------
	resp.setAnswer(netip.MustParseAddr(dnsConcHubA))
	if !top.pingUntil(concInner, 20*time.Second) {
		t.Fatalf("tunnel never came up after the name began resolving to %s (R70 first-resolve install)\n--- edge ---\n%s\n--- conc ---\n%s",
			dnsConcHubA, edge.log(), conc.log())
	}
	if !waitLogContains(edge, "first endpoint resolution", 5*time.Second) {
		t.Fatalf("edge came up but did not log the R70 first-resolve endpoint install\n--- edge ---\n%s", edge.log())
	}
	if mbps := top.iperf3Mbps(t, concInner, 3); mbps <= 0 {
		t.Fatalf("post-install throughput non-positive (%.2f Mbit/s)", mbps)
	}
	t.Logf("(1) first resolve -> %s installed the endpoint (R70); tunnel up and carrying traffic", dnsConcHubA)

	// -------- (2)+(3) concentrator IP changes mid-session; edge re-resolves and repoints -----
	queriesBefore := resp.queryCount()
	resp.setAnswer(netip.MustParseAddr(dnsConcHubB))
	concDelAddr(top, edgePath.concVeth, dnsConcHubA) // decommission the old address for real
	t.Logf("(2) renumbered concentrator: DNS %q now answers %s; %s flushed from the concentrator namespace",
		dnsHubHost, dnsConcHubB, dnsConcHubA)

	// -------- (4) tunnel survives — traffic actually resumes (D32 guard) --------
	// Bound: the [dns] poll cadence (1s) OR the out-of-band liveness-loss re-resolve, plus the
	// hub-failover settle dwell and a fresh handshake, plus margin for the shared-CPU fixture.
	repointBound := time.Duration(P1RecoverySeconds)*time.Second + 12*time.Second
	if !top.pingUntil(concInner, repointBound) {
		t.Fatalf("tunnel did not repoint to the renumbered concentrator %s within %v (old %s is flushed, so this proves the edge did NOT re-resolve)\n--- edge ---\n%s\n--- conc ---\n%s",
			dnsConcHubB, repointBound, dnsConcHubA, edge.log(), conc.log())
	}
	if !waitLogContains(edge, "active concentrator endpoint re-resolved", 5*time.Second) {
		t.Fatalf("tunnel recovered but the edge did not log the re-resolve repoint (expected exactly one SetPeerRemote to the new IP)\n--- edge ---\n%s", edge.log())
	}
	// D32 regression guard: bytes must actually move after the repoint, not merely a handshake.
	// concHubA is gone, so a positive transfer is only possible over concHubB with a
	// re-baselined resequencer.
	if mbps := top.iperf3Mbps(t, concInner, 3); mbps <= 0 {
		t.Fatalf("post-renumber throughput non-positive (%.2f Mbit/s) — a handshake without a resequencer re-baseline is the D32 defect", mbps)
	}

	// Hermeticity: the re-resolution genuinely went through the in-netns responder (its query
	// count grew), so no external DNS egress carried the renumber.
	if grew := resp.queryCount() - queriesBefore; grew == 0 {
		t.Fatalf("the in-netns DNS responder served no queries across the renumber; resolution did not go through the hermetic responder")
	}
	t.Logf("(3)+(4) edge re-resolved to %s, repointed, and traffic resumed with a re-baselined resequencer (D32 guard)", dnsConcHubB)
}

// The two concentrator hub addresses (its "public IP" before and after the mid-session
// renumber), both in the single edge path's /24 so one edge path reaches either.
const (
	dnsConcHubA = "10.100.7.2"
	dnsConcHubB = "10.100.7.3"
	// dnsHubHost is the concentrator's DNS name the edge is configured with; multi-label so a
	// resolv.conf ndots:0 query resolves it as absolute without a search-domain round.
	dnsHubHost = "concentrator.wanbond.test"
	// dnsPollInterval is the edge's [dns] re-resolution cadence for this test — far tighter
	// than the 30s production default so the scenario's resolve/re-resolve beats land within a
	// bounded test window. The out-of-band liveness-loss trigger repoints even faster.
	dnsPollInterval = "1s"
)

// writeDNSConfigs writes the edge and concentrator TOML for the DNS scenario. The edge names
// the concentrator by HOSTNAME (dns = true, [dns] resolver = "system") and its single path
// carries NO dest_addr, so the ONLY source of the concentrator endpoint is the DNS-resolved
// WireGuard peer endpoint — a resolve failure therefore boots the peer endpoint-less (0). The
// concentrator is multi-homed on both hub addresses so it serves the name before AND after the
// renumber. Both daemons log at "info" so the boot-defer marker (Info) and the hub-failover
// install/repoint markers (Warn) are observable.
func writeDNSConfigs(t *testing.T, edgePath pathSpec) (edgeCfg, concCfg string) {
	t.Helper()
	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	dir := t.TempDir()

	edgeCfg = writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = %q

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
dns = true
allowed_ips = ["%s/32"]

[dns]
resolver = "system"
poll_interval = %q
timeout = "2s"

[log]
level = "info"
`, psk, edgePath.name, edgePath.edgeIP, edgePriv, concPub, dnsHubHost, listenPort, concInner, dnsPollInterval))

	concCfg = writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "hubA"
source_addr = "%s"

[[paths]]
name = "hubB"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, dnsConcHubA, dnsConcHubB, concPriv, listenPort, edgePub, edgeInner))

	return edgeCfg, concCfg
}

// concAddAddr / concDelAddr add and remove a /24 address on the named concentrator-side veth,
// inside the concentrator network namespace — the "move the address in the conc namespace"
// half of the mid-session renumber. concAddAddr provisions the second hub address before the
// concentrator starts; concDelAddr decommissions the old one after DNS is cut over, so a
// post-renumber transfer is possible ONLY if the edge genuinely repointed.
func concAddAddr(top *Topology, concVeth, ip string) {
	top.nsenter("ip", "addr", "add", ip+"/24", "dev", concVeth)
}

func concDelAddr(top *Topology, concVeth, ip string) {
	top.nsenter("ip", "addr", "del", ip+"/24", "dev", concVeth)
}

// startProcEnv is startProc with extra environment variables appended to the inherited env
// (used to force GODEBUG=netdns=go on the edge daemon so the pure-Go resolver reads the
// bind-mounted resolv.conf). It otherwise mirrors startProc exactly, including the SIGTERM/
// SIGKILL cleanup that reaps the child and its output copier before the final log() read.
func (top *Topology) startProcEnv(t *testing.T, name string, extraEnv []string, argv ...string) *proc {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s (%v): %v", name, argv, err)
	}
	p := &proc{cmd: cmd, name: name, output: out}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return p
}

// waitLogContains polls a process's captured output until it contains want, or the deadline
// elapses. Used to observe the boot-defer / first-resolve-install / re-resolve-repoint log
// markers the daemon emits, whose appearance is the un-flaky positive signal for each beat.
func waitLogContains(p *proc, want string, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if strings.Contains(p.log(), want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return strings.Contains(p.log(), want)
}

// resolvConfTarget is the file the Go resolver reads for its nameserver list. The scenario
// bind-mounts a private copy over it inside this process's mount namespace so the override is
// invisible to the host and every other test.
const resolvConfTarget = "/etc/resolv.conf"

// mountResolvConf bind-mounts a private resolv.conf — pointing the resolver at nameserverIP —
// over /etc/resolv.conf in THIS process's mount namespace (created by the TestMain re-exec's
// unshare -m). The override never touches the host's file (unshare makes the mount private),
// and it is unmounted on cleanup. ndots:0 makes the multi-label hub hostname a
// resolved-as-absolute query; attempts/timeout keep a miss snappy.
func mountResolvConf(t *testing.T, nameserverIP string) {
	t.Helper()
	body := fmt.Sprintf("nameserver %s\noptions ndots:0 attempts:1 timeout:1\n", nameserverIP)
	src := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatalf("write private resolv.conf: %v", err)
	}
	if err := syscall.Mount(src, resolvConfTarget, "", syscall.MS_BIND, ""); err != nil {
		t.Fatalf("bind-mount %s over %s (needs the TestMain mount namespace): %v", src, resolvConfTarget, err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(resolvConfTarget, syscall.MNT_DETACH) })
}

// dnsResponder is a minimal in-test UDP DNS server (dnsmessage-based, RFC 1035 wire format)
// that answers A queries for a single host with a mutable address — and NXDOMAIN when no
// answer is set (the "unresolvable" boot state). It is the netns-local answer source that
// makes the scenario hermetic: it binds inside the edge network namespace and the edge's
// pure-Go resolver dials it over plain UDP, so no name-resolution packet ever leaves the
// sandbox. AAAA queries get NOERROR/NODATA (the edge paths are v4-only), so LookupNetIP("ip")
// resolves to the v4 answer without one family's miss failing the whole lookup.
type dnsResponder struct {
	conn *net.UDPConn
	host string // normalized (lower-case, no trailing dot) name this responder answers for

	mu      sync.Mutex
	answer  netip.Addr // invalid zero value => answer NXDOMAIN (unresolvable)
	queries int
}

// startDNSResponder binds a UDP DNS responder at listenAddr (a "host:port", conventionally
// port 53) for host, launches its serve loop, and registers a cleanup that closes the socket.
// It starts UNRESOLVABLE; setAnswer installs (or replaces) the A answer.
func startDNSResponder(t *testing.T, listenAddr, host string) *dnsResponder {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		t.Fatalf("resolve responder listen addr %q: %v", listenAddr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("bind DNS responder on %q: %v", listenAddr, err)
	}
	r := &dnsResponder{conn: conn, host: dnsNormalizeName(host)}
	t.Cleanup(func() { _ = conn.Close() })
	go r.serve()
	return r
}

// setAnswer sets the A answer this responder returns for its host (an invalid zero Addr
// reverts it to NXDOMAIN). Safe to call while the serve loop is running.
func (r *dnsResponder) setAnswer(a netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = a
}

// queryCount returns the number of queries served so far (a hermeticity probe: it must grow
// across the renumber, proving re-resolution went through this in-netns responder).
func (r *dnsResponder) queryCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queries
}

// serve reads UDP queries until the socket is closed, replying to each. A read error after
// Close ends the loop.
func (r *dnsResponder) serve() {
	buf := make([]byte, 512)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed on cleanup
		}
		reply, ok := r.buildReply(buf[:n])
		if !ok {
			continue
		}
		_, _ = r.conn.WriteToUDP(reply, from)
	}
}

// buildReply parses a query and packs the response: the A answer (or NXDOMAIN) for an A query
// on the served host, NOERROR/NODATA for AAAA (or any other type on the served host), and
// NXDOMAIN for any other name. It echoes the query ID and question, as the resolver requires
// to match the reply. ok is false only for an unparseable datagram (dropped).
func (r *dnsResponder) buildReply(query []byte) ([]byte, bool) {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil {
		return nil, false
	}
	q, err := p.Question()
	if err != nil {
		return nil, false
	}

	r.mu.Lock()
	r.queries++
	answer := r.answer
	r.mu.Unlock()

	// NXDOMAIN for a name we do not serve, or for the served name while it is unresolvable
	// (no answer set). Otherwise NOERROR — with an A record for an A query, NODATA for AAAA.
	nameMatches := dnsNormalizeName(q.Name.String()) == r.host
	rcode := dnsmessage.RCodeSuccess
	if !nameMatches || !answer.IsValid() {
		rcode = dnsmessage.RCodeNameError
	}

	rh := dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RCode: rcode}
	b := dnsmessage.NewBuilder(nil, rh)
	if err := b.StartQuestions(); err != nil {
		return nil, false
	}
	if err := b.Question(q); err != nil {
		return nil, false
	}
	// Emit an A answer only for a matching A query with a valid answer; AAAA and everything
	// else on the served host stay NOERROR/NODATA (no answer records).
	if rcode == dnsmessage.RCodeSuccess && q.Type == dnsmessage.TypeA {
		if err := b.StartAnswers(); err != nil {
			return nil, false
		}
		ah := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 5}
		if err := b.AResource(ah, dnsmessage.AResource{A: answer.As4()}); err != nil {
			return nil, false
		}
	}
	msg, err := b.Finish()
	if err != nil {
		return nil, false
	}
	return msg, true
}

// dnsNormalizeName lower-cases a DNS name and strips a single trailing dot, so the responder's
// host match is case- and FQDN-insensitive (the resolver queries the fully-qualified form).
func dnsNormalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}
