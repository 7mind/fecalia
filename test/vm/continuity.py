#!/usr/bin/env python3
"""Bidirectional TCP and voice through capacity changes and independent WAN outages."""

import argparse
import concurrent.futures
import json
import hashlib
from pathlib import Path
import time

from lab import GUESTS, Lab
from benchmark import apply_profile, profile_from, provision
from continuity_gates import evaluate


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profiles") / "basic.json")
    args = parser.parse_args()
    lab = Lab()
    lab.acquire()
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-continuity")
    output.mkdir()
    profile = profile_from(args.profile)
    manifest = {"binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "profile": profile, "phases": []}
    apply_profile(lab, profile)
    provision(lab, args.binary, "adaptive")
    for guest in GUESTS:
        address = "10.77.0.1" if guest == "hub" else "10.77.0.2"
        lab.put(guest, Path(__file__).with_name("voice.py"), "/root/voice.py")
        lab.execute(guest, f"nohup python3 /root/voice.py server {address} > /root/voice-server.log 2>&1 < /dev/null & echo $! > /root/voice.pid")
    lab.execute("hub", "iperf3 -s -1 -D -J -B 10.77.0.1")
    try:
        with concurrent.futures.ThreadPoolExecutor() as pool:
            tcp = pool.submit(lab.execute, "edge", "iperf3 -c 10.77.0.1 --bidir -l 1K -t 65 -J --get-server-output", capture_output=True)
            voices = {guest: pool.submit(lab.execute, guest, f"python3 /root/voice.py client 10.77.0.{2 if guest == 'hub' else 1} --seconds 65", capture_output=True) for guest in GUESTS}
            start = time.monotonic()
            phases = [(15, "starlink-standby", 1, 0, True), (25, "starlink-outage", 1, 100, True),
                      (30, "starlink-recovery", 1, 0, False), (40, "lte-outage", 2, 100, False),
                      (45, "lte-recovery", 2, 0, False), (55, "starlink-loss", 1, 1, False)]
            for offset, name, lane, loss, standby in phases:
                time.sleep(max(0, start+offset-time.monotonic()))
                print(name, flush=True)
                conditions = {guest: dict(profile[guest][str(lane)]) for guest in GUESTS}
                for condition in conditions.values():
                    if standby:
                        condition["rate"] = min(condition["rate"], 0.5)
                    condition["loss"] = max(condition["loss"], loss)
                changes = [pool.submit(lab.impair, guest, lane, **conditions[guest]) for guest in GUESTS]
                for change in changes:
                    change.result()
                manifest["phases"].append({"name": name, "at_seconds": time.monotonic()-start, "lane": lane, "conditions": conditions})
                (output / "scenario.json").write_text(json.dumps(manifest, indent=2))
                for guest in GUESTS:
                    metrics = lab.execute(guest, "curl -sf http://127.0.0.1:9090/metrics", capture_output=True).stdout
                    (output / f"{name}-{guest}-metrics.txt").write_text(metrics)
            tcp_result = json.loads(tcp.result().stdout)
            (output / "tcp.json").write_text(json.dumps(tcp_result, indent=2))
            reports = {}
            for guest, future in voices.items():
                report = json.loads(future.result().stdout)
                (output / f"{guest}-voice.json").write_text(json.dumps(report))
                reports[guest] = {key: value for key, value in report.items() if key != "samples"}
                (output / f"{guest}-daemon.log").write_text(lab.execute(guest, "cat /root/wanbond.log", capture_output=True).stdout)
            (output / "summary.json").write_text(json.dumps(reports, indent=2))
            print(json.dumps(reports, indent=2), flush=True)
            print(output, flush=True)
            failures = evaluate(output)
            assert not failures, "; ".join(failures)
    finally:
        for guest in GUESTS:
            lab.execute(guest, "if test -f /root/voice.pid; then kill $(cat /root/voice.pid) 2>/dev/null || true; rm /root/voice.pid; fi")



if __name__ == "__main__":
    main()
