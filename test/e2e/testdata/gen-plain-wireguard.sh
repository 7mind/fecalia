#!/usr/bin/env bash
# Generate a genuine PLAIN WireGuard handshake+transport pcap between two netns
# endpoints using the kernel wireguard module. Run under `unshare -Urmn`.
set -euo pipefail
OUT="$1"
WG=$(command -v wg)

ip link set lo up

# peer ("concentrator") netns via a holder process, addressed by PID
unshare -n sleep 600 &
HPID=$!
for i in $(seq 1 100); do [ -e /proc/$HPID/ns/net ] && break; sleep 0.05; done

# veth between the two netns
ip link add wbe type veth peer name wbc
ip link set wbc netns $HPID
ip addr add 10.9.0.1/24 dev wbe
ip link set wbe up
nsenter -t $HPID -n ip link set lo up
nsenter -t $HPID -n ip addr add 10.9.0.2/24 dev wbc
nsenter -t $HPID -n ip link set wbc up

# keys
PRIV_A=$("$WG" genkey); PUB_A=$(echo "$PRIV_A" | "$WG" pubkey)
PRIV_B=$("$WG" genkey); PUB_B=$(echo "$PRIV_B" | "$WG" pubkey)
echo "$PRIV_A" > /tmp/a.key; echo "$PRIV_B" > /tmp/b.key

# wg endpoint A in the current (edge) netns
ip link add wg0 type wireguard
ip addr add 192.168.99.1/24 dev wg0
"$WG" set wg0 private-key /tmp/a.key listen-port 51820 \
    peer "$PUB_B" endpoint 10.9.0.2:51821 allowed-ips 192.168.99.2/32 persistent-keepalive 5
ip link set wg0 up

# wg endpoint B in the peer (concentrator) netns
nsenter -t $HPID -n ip link add wg0 type wireguard
nsenter -t $HPID -n ip addr add 192.168.99.2/24 dev wg0
nsenter -t $HPID -n "$WG" set wg0 private-key /tmp/b.key listen-port 51821 \
    peer "$PUB_A" endpoint 10.9.0.1:51820 allowed-ips 192.168.99.1/32
nsenter -t $HPID -n ip link set wg0 up

# capture the outer WG UDP on the edge veth
tcpdump -i wbe -n -p -U -w "$OUT" 'udp portrange 51820-51821' &
TDPID=$!
sleep 2.0

# drive a handshake + transport traffic through the tunnel
ping -c 12 -i 0.3 192.168.99.2 || true
sleep 3.0

kill -TERM $TDPID; sleep 0.5; wait $TDPID 2>/dev/null || true
kill $HPID 2>/dev/null || true
echo "wrote $OUT"
