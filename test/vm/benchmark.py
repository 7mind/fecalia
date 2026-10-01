#!/usr/bin/env python3
"""Performance-Effectual-GoodCommunication: actual daemons, kernels and emulated WANs."""

import argparse
import base64
import concurrent.futures
import hashlib
import json
from pathlib import Path
import secrets
import time

from lab import GUESTS, Lab

# Emitted explicitly: a binary that still carries the removed transports selects
# another one when the key is omitted.
POLICY = "adaptive"


def profile_from(path):
    profile = json.loads(path.read_text())
    if set(profile) != set(GUESTS) or any(set(profile[g]) != {"1", "2"} for g in GUESTS):
        raise ValueError("profile must define hub/edge and WANs 1/2")
    return profile


def apply_profile(lab, profile):
    with concurrent.futures.ThreadPoolExecutor() as pool:
        futures = [pool.submit(lab.impair, guest, int(lane), **condition)
                   for guest, lanes in profile.items() for lane, condition in lanes.items()]
        for future in futures:
            future.result()


def provision(lab, binary):
    binary = binary.resolve(strict=True)
    if not binary.is_file():
        raise ValueError("candidate must be a regular executable file")
    keys = {}
    for guest in GUESTS:
        keys[guest] = lab.execute(guest, "wg genkey", capture_output=True).stdout.strip()
    public = {guest: lab.execute(guest, "wg pubkey", input=key, capture_output=True).stdout.strip()
              for guest, key in keys.items()}
    psk = base64.b64encode(secrets.token_bytes(32)).decode()
    for guest in GUESTS:
        hub = guest == "hub"
        other = "edge" if hub else "hub"
        lab.execute(guest, "if test -f /root/wanbond.pid; then kill $(cat /root/wanbond.pid) 2>/dev/null || true; fi; sleep 1")
        lab.put(guest, binary, "/root/wanbond")
        paths = ('[[paths]]\nname="wan"\nsource_addr="0.0.0.0"\n' if hub else
                 ''.join(f'[[paths]]\nname="wan{i}"\nsource_addr="10.77.{i}.2"\n' for i in (1, 2)))
        config = f'''role="{"concentrator" if hub else "edge"}"
psk="{psk}"
bind="source"
{paths}
[wireguard]
private_key="{keys[guest]}"
listen_port={51820 if hub else 0}
[[wireguard.peers]]
public_key="{public[other]}"
allowed_ips=["10.77.0.{2 if hub else 1}/32"]
'''
        if not hub:
            config += 'endpoint="10.77.200.1:51820"\n'
        config += f'''[scheduler]
policy="{POLICY}"
[metrics]
listen="127.0.0.1:9090"
[liveness]
down_after="600ms"
[log]
level="info"
'''
        lab.execute(guest, "umask 077; cat > /root/wanbond.toml", input=config)
        lab.execute(guest, "chmod 700 /root/wanbond; nohup /root/wanbond --config /root/wanbond.toml > /root/wanbond.log 2>&1 < /dev/null & echo $! > /root/wanbond.pid")
    for guest in GUESTS:
        address = 1 if guest == "hub" else 2
        lab.execute(guest, f"""set -eu
for n in $(seq 1 40); do
  ip link show wanbond0 >/dev/null 2>&1 && break
  if ! kill -0 $(cat /root/wanbond.pid) 2>/dev/null; then cat /root/wanbond.log >&2; exit 1; fi
  sleep 0.25
done
ip addr replace 10.77.0.{address}/24 dev wanbond0
ip link set wanbond0 up
ip -j link show wanbond0
""")
    time.sleep(3)
    lab.execute("edge", "ping -c 3 -W 3 10.77.0.1")


def counters(lab):
    return json.loads(lab.execute("edge", "ip -s -j link show", capture_output=True).stdout)


def measure(lab, output, name, seconds, warmup, reverse):
    before = counters(lab)
    for guest in GUESTS:
        (output / f"{name}-before-{guest}-metrics.txt").write_text(lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout)
    lab.execute("hub", "iperf3 -s -1 -D --pidfile /root/iperf.pid -B 10.77.0.1")
    result = lab.execute("edge", f"iperf3 -c 10.77.0.1 -t {seconds} -O {warmup} -J {'-R' if reverse else ''}", capture_output=True)
    after = counters(lab)
    for guest in GUESTS:
        (output / f"{name}-after-{guest}-metrics.txt").write_text(lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout)
    data = json.loads(result.stdout)
    (output / f"{name}.json").write_text(json.dumps(data, indent=2))
    key = "rx" if reverse else "tx"
    byte_counts = {}
    for lane in (1, 2):
        def total(snapshot):
            entry = next(e for e in snapshot if e["ifname"] == f"eth{lane}")
            return entry["stats64"][key]["bytes"]
        byte_counts[f"wan{lane}"] = total(after) - total(before)
    summary = {"mbit_s": data["end"]["sum_received"]["bits_per_second"] / 1e6, "bytes": byte_counts}
    print(name, json.dumps(summary), flush=True)
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--seconds", type=int, default=20)
    parser.add_argument("--warmup", type=int, default=5, help="convergence period; retained as omitted intervals in raw iperf output")
    parser.add_argument("--idle-seconds", type=int, default=0, help="leave the tunnel idle before measuring startup from sparse feedback")
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profiles") / "basic.json")
    args = parser.parse_args()
    if args.idle_seconds < 0:
        parser.error("--idle-seconds must be nonnegative")
    lab = Lab()
    lab.acquire()
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-" + POLICY)
    output.mkdir()
    profile = profile_from(args.profile)
    apply_profile(lab, profile)
    provision(lab, args.binary)
    summary = {"binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "policy": POLICY, "profile": profile, "warmup_seconds": args.warmup, "measurement_seconds": args.seconds, "idle_seconds": args.idle_seconds}
    try:
        if args.idle_seconds:
            for guest in GUESTS:
                (output / f"idle-start-{guest}-metrics.txt").write_text(lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout)
            time.sleep(args.idle_seconds)
        for reverse in (False, True):
            name = "downlink" if reverse else "uplink"
            summary[name] = measure(lab, output, name, args.seconds, args.warmup, reverse)
    finally:
        for guest in GUESTS:
            for path in ("wanbond.log", "lab-packages.txt"):
                (output / f"{guest}-{path}").write_text(lab.execute(guest, f"cat /root/{path}", capture_output=True).stdout)
            (output / f"{guest}-metrics.txt").write_text(lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout)
        (output / "summary.json").write_text(json.dumps(summary, indent=2))
        print(output, flush=True)
    for name in ("uplink", "downlink"):
        measurement = summary[name]
        sender = "edge" if name == "uplink" else "hub"
        capacity = sum(lane["rate"] for lane in profile[sender].values())
        assert measurement["mbit_s"] >= 0.75*capacity, f"{name}: requires >=75% of {capacity} Mbit/s combined wire capacity"
        assert min(measurement["bytes"].values()) > 100_000, f"{name}: both WANs must carry bulk traffic"


if __name__ == "__main__":
    main()
