#!/usr/bin/env python3
"""Constant-rate UDP goodput through the tunnel, in each direction."""

import argparse
import hashlib
import json
from pathlib import Path
import time

from lab import GUESTS, Lab
from benchmark import apply_profile, counters, profile_from, provision

# The tunnel MTU is 1339 bytes; a full datagram occupies 1500 bytes of IP on
# the WAN and 1514 bytes of the emulated link's rate.
UDP_PAYLOAD = 1339 - 28
WIRE_BYTES = 1514
REQUIRED_SHARE = 0.80


def measure(lab, output, name, offered, seconds, warmup, reverse):
    before = counters(lab)
    lab.execute("hub", "iperf3 -s -1 -D -J --pidfile /root/iperf.pid -B 10.77.0.1")
    # iperf3's --omit makes its UDP totals inconsistent (received bytes and
    # packet counts disagree), so the warmup is excluded here from the
    # receiver's own interval reports.
    result = lab.execute("edge", f"iperf3 -c 10.77.0.1 -u -b {offered:.4f}M -l {UDP_PAYLOAD} -w 4M -t {warmup + seconds} -J --get-server-output {'-R' if reverse else ''}", capture_output=True)
    after = counters(lab)
    for guest in GUESTS:
        (output / f"{name}-after-{guest}-metrics.txt").write_text(lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout)
    data = json.loads(result.stdout)
    (output / f"{name}.json").write_text(json.dumps(data, indent=2))
    receiver = data if reverse else data["server_output_json"]
    measured = [interval["sum"] for interval in receiver["intervals"] if interval["sum"]["start"] >= warmup - 0.5]
    assert len(measured) >= seconds - 1, f"{name}: receiver reported {len(measured)} measured intervals"
    duration = measured[-1]["end"] - measured[0]["start"]
    key = "rx" if reverse else "tx"
    byte_counts = {}
    for lane in (1, 2):
        def wan(snapshot):
            entry = next(e for e in snapshot if e["ifname"] == f"eth{lane}")
            return entry["stats64"][key]["bytes"]
        byte_counts[f"wan{lane}"] = wan(after) - wan(before)
    summary = {"offered_mbit_s": offered, "mbit_s": sum(interval["bytes"] for interval in measured) * 8 / duration / 1e6,
               "lost_percent": data["end"]["sum"]["lost_percent"], "bytes": byte_counts}
    print(name, json.dumps(summary), flush=True)
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--seconds", type=int, default=20)
    parser.add_argument("--warmup", type=int, default=10, help="convergence period; retained as omitted intervals in raw iperf output")
    parser.add_argument("--idle-seconds", type=int, default=0)
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profiles") / "basic.json")
    args = parser.parse_args()
    lab = Lab()
    lab.acquire()
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-udp")
    output.mkdir()
    profile = profile_from(args.profile)
    apply_profile(lab, profile)
    provision(lab, args.binary, "adaptive")
    summary = {"binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "profile": profile,
               "warmup_seconds": args.warmup, "measurement_seconds": args.seconds, "idle_seconds": args.idle_seconds}
    capacity = {}
    try:
        time.sleep(args.idle_seconds)
        for reverse in (False, True):
            name = "downlink" if reverse else "uplink"
            sender = "hub" if reverse else "edge"
            capacity[name] = sum(lane["rate"] for lane in profile[sender].values())
            # Offer what full datagrams could carry if the links held nothing else.
            offered = capacity[name] * UDP_PAYLOAD / WIRE_BYTES
            summary[name] = measure(lab, output, name, offered, args.seconds, args.warmup, reverse)
    finally:
        for guest in GUESTS:
            (output / f"{guest}-wanbond.log").write_text(lab.execute(guest, "cat /root/wanbond.log", capture_output=True).stdout)
        (output / "summary.json").write_text(json.dumps(summary, indent=2))
        print(output, flush=True)
    for name in ("uplink", "downlink"):
        measurement = summary[name]
        assert measurement["mbit_s"] >= REQUIRED_SHARE*capacity[name], f"{name}: requires >={REQUIRED_SHARE:.0%} of {capacity[name]} Mbit/s combined wire capacity"
        assert min(measurement["bytes"].values()) > 100_000, f"{name}: both WANs must carry traffic"


if __name__ == "__main__":
    main()
