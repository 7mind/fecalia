# wanbond — install and operations

wanbond ships as a single static binary. One binary serves both roles — the
**edge** (the mobile Linux box bonding the WAN uplinks) and the **concentrator**
(the public-IP VPS terminating the tunnel); the role is selected by the `role`
key in the config file, never by which binary is invoked.

## 1. Build the release binaries

From the repo root, inside the dev shell:

```sh
nix develop -c just release
```

This cross-compiles `cmd/wanbond` with `CGO_ENABLED=0` (fully static, no libc
dependency) for both deployment architectures into `dist/`:

- `dist/wanbond-linux-amd64` — x86_64 (typical OCI/AWS VPS concentrator)
- `dist/wanbond-linux-arm64` — aarch64 (ARM edge boxes, Ampere VPSes)

Verify with `file dist/*`: both must report `statically linked`. Alternatively
`nix build` produces the host-architecture binary via the flake package.

## 2. Install the binary

On each host (pick the artifact matching `uname -m`):

```sh
install -m 0755 wanbond-linux-<arch> /usr/local/bin/wanbond
wanbond version   # prints the stamped build version
```

## 3. Write the config file — 0600 REQUIRED

The daemon takes exactly one flag: `--config <path>`. The systemd units use
`/etc/wanbond/edge.toml` and `/etc/wanbond/concentrator.toml`.

The file holds the WireGuard private key and the outer-control PSK, so
`config.Load` **refuses any file whose permission bits are not exactly `0600`**
(`insecure permissions` error at startup). Create it as:

```sh
mkdir -p /etc/wanbond
touch /etc/wanbond/edge.toml
chown root:root /etc/wanbond/edge.toml
chmod 0600 /etc/wanbond/edge.toml
```

### Edge config (`/etc/wanbond/edge.toml`)

```toml
role = "edge"
psk = "<base64 32-byte outer-control PSK, same on both ends>"

# One [[paths]] block per WAN uplink. source_addr pins the path's UDP socket
# to the local source IP the upstream router routes out the intended WAN.
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"

[[paths]]
name = "5g"
source_addr = "192.168.2.10"
# dest_addr = "203.0.113.7:51820"  # optional per-path concentrator endpoint;
                                   # omit when one public IP fronts all uplinks

[wireguard]
private_key = "<base64 edge private key>"

[[wireguard.peers]]
public_key = "<base64 concentrator public key>"
endpoint = "203.0.113.7:51820"     # required on the edge
allowed_ips = ["10.77.0.1/32"]     # concentrator's inner tunnel address

[metrics]
listen = "127.0.0.1:9090"          # loopback only; the daemon rejects anything else

[log]
level = "info"
```

### Concentrator config (`/etc/wanbond/concentrator.toml`)

```toml
role = "concentrator"
psk = "<same PSK as the edge>"

# The concentrator learns the edge's real per-path endpoints from
# authenticated traffic; it only needs its own bind address.
[[paths]]
name = "wan0"
source_addr = "10.0.0.5"           # the VPS's primary (private) interface IP

[wireguard]
private_key = "<base64 concentrator private key>"
listen_port = 51820                # required on the concentrator

[[wireguard.peers]]
public_key = "<base64 edge public key>"
allowed_ips = ["10.77.0.2/32"]     # edge's inner tunnel address; no endpoint —
                                   # the concentrator roams the edge dynamically

[metrics]
listen = "127.0.0.1:9090"

[log]
level = "info"
```

Optional on both ends: an `[amnezia]` obfuscation block (all-or-nothing — when
used, `jc`, `jmin`, `jmax`, `s1`, `s2` must all be set, identically on both
ends). Omit it entirely for plain WireGuard framing.

Generate keys with standard WireGuard tooling (`wg genkey | tee k | wg pubkey`)
and the PSK with `head -c 32 /dev/urandom | base64`.

## 4. systemd units

Unit files live in `packaging/systemd/`:

```sh
cp packaging/systemd/wanbond-edge.service /etc/systemd/system/          # edge box
cp packaging/systemd/wanbond-concentrator.service /etc/systemd/system/  # VPS
systemctl daemon-reload
systemctl enable --now wanbond-edge      # or wanbond-concentrator
```

- `systemctl reload wanbond-<role>` sends SIGHUP: the daemon re-reads the
  config and applies the **path diff** (add/remove uplinks) without tearing
  down the live tunnel. A bad reload is logged and the running config kept.
- `Restart=on-failure` restarts the daemon if the tunnel engine tears down
  unexpectedly (the process exits non-zero in that case by design).
- The units run with `CapabilityBoundingSet=CAP_NET_ADMIN` (TUN creation) plus
  standard hardening. If you set `wireguard.listen_port` below 1024, add
  `CAP_NET_BIND_SERVICE` to the bounding set.

### Interface addressing and routing (operator-owned)

The daemon creates the TUN interface (`wanbond0`) and owns ONLY the tunnel
engine — **it never assigns addresses or routes** (wg-quick style, no
privileged shell-outs). Configure them with a systemd drop-in on each end,
using the inner addresses from `allowed_ips`:

```sh
systemctl edit wanbond-edge    # or wanbond-concentrator
```

```ini
[Service]
ExecStartPost=/usr/sbin/ip address add 10.77.0.2/24 dev wanbond0
ExecStartPost=/usr/sbin/ip link set wanbond0 up
```

(Concentrator: `10.77.0.1/24`. Adjust the binary path to `command -v ip`.)

## 5. Firewall

### Concentrator: UDP listen port

Allow the WireGuard UDP port in from the WAN (and, on OCI, in the subnet's
security list / NSG as well — both layers must pass):

```sh
iptables -I INPUT -p udp --dport 51820 -j ACCEPT
```

### Concentrator: tunnel-interface ACCEPT — REQUIRED (OCI default-REJECT caveat)

The concentrator **must ACCEPT traffic arriving on the wanbond tunnel
interface, ordered ahead of any default REJECT rule**:

```sh
iptables -I INPUT -i wanbond0 -j ACCEPT
```

Why this is required: Oracle Cloud (OCI) images ship an INPUT chain ending in

```
-A INPUT -j REJECT --reject-with icmp-host-prohibited
```

which silently applies to the tunnel interface too. ICMP echo is allowed by an
earlier rule, so **ping through the tunnel works while any TCP connection
through the tunnel fails with a confusing `No route to host`** (the REJECT's
ICMP host-prohibited answer). This exact failure was hit during P0 real-host
testing. `iptables -I` (insert at head) places the ACCEPT ahead of the appended
(`-A`) REJECT; verify the ordering with `iptables -S INPUT` — the tunnel ACCEPT
must appear before the `-j REJECT` line. The same applies to any distro or
provider whose default firewall ends INPUT with REJECT/DROP (e.g. firewalld
zones): treat "tunnel ACCEPT ahead of the default reject" as a required
concentrator deployment step.

Persist the rules across reboots with your distro's mechanism
(`netfilter-persistent save` on Debian/Ubuntu, `service iptables save` or a
firewalld permanent rule on EL). Note the rule references the tunnel interface
by name, so it only matches while `wanbond0` exists; inserting it at boot
(before the daemon starts) is harmless.

### Edge

Outbound UDP to the concentrator's `endpoint` (and any per-path `dest_addr`)
must be open on every uplink; no inbound rules are needed — the edge initiates.

## 6. Observability

Each daemon serves Prometheus metrics on the loopback-only `[metrics] listen`
address (`curl -s http://127.0.0.1:9090/metrics`). Logs go to stderr →
`journalctl -u wanbond-<role>`.

## 7. MTU

The daemon sets the TUN MTU itself from the bonded-overhead budget (see
`docs/p1-mtu.md`); do not override it. If an on-path MTU below the default
1500 is in play, see the TCP MSS-clamp guidance in that document.
