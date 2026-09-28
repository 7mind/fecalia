#!/usr/bin/env python3
"""Bidirectional TCP and voice through capacity changes and independent WAN outages."""

import argparse
import concurrent.futures
import json
import hashlib
from pathlib import Path
import time

from lab import GUESTS, Lab
from benchmark import provision


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    lab = Lab()
    lab.acquire()
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-continuity")
    output.mkdir()
    manifest = {"binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(), "phases": []}
    for guest in GUESTS:
        for lane, rate, delay in ((1, 2, 15), (2, 6, 25)):
            lab.impair(guest, lane, rate, delay, 0, 0)
    provision(lab, args.binary, "adaptive")
    for guest in GUESTS:
        address = "10.77.0.1" if guest == "hub" else "10.77.0.2"
        lab.put(guest, Path(__file__).with_name("voice.py"), "/root/voice.py")
        lab.execute(guest, f"nohup python3 /root/voice.py server {address} > /root/voice-server.log 2>&1 < /dev/null & echo $! > /root/voice.pid")
    lab.execute("hub", "iperf3 -s -1 -D -B 10.77.0.1")
    try:
        with concurrent.futures.ThreadPoolExecutor() as pool:
            tcp = pool.submit(lab.execute, "edge", "iperf3 -c 10.77.0.1 --bidir -t 65 -J", capture_output=True)
            voices = {guest: pool.submit(lab.execute, guest, f"python3 /root/voice.py client 10.77.0.{2 if guest == 'hub' else 1} --seconds 65", capture_output=True) for guest in GUESTS}
            start = time.monotonic()
            phases = [(15, "starlink-standby", 1, 0.5, 15, 0), (25, "starlink-outage", 1, 0.5, 15, 100),
                      (30, "starlink-recovery", 1, 2, 15, 0), (40, "lte-outage", 2, 6, 25, 100),
                      (45, "lte-recovery", 2, 6, 25, 0), (55, "starlink-loss", 1, 2, 15, 1)]
            for offset, name, lane, rate, delay, loss in phases:
                time.sleep(max(0, start+offset-time.monotonic()))
                print(name, flush=True)
                changes = [pool.submit(lab.impair, guest, lane, rate, delay, loss, 0) for guest in GUESTS]
                for change in changes:
                    change.result()
                manifest["phases"].append({"name": name, "at_seconds": time.monotonic()-start, "lane": lane, "rate_mbit": rate, "one_way_delay_ms": delay, "loss_percent": loss})
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
            assert "error" not in tcp_result, tcp_result.get("error")
            assert len(tcp_result["intervals"]) >= 60, "TCP did not survive the complete scenario"
            for key in ("sum", "sum_bidir_reverse"):
                for begin, end in ((26, 30), (41, 45)):
                    intervals = [entry[key] for entry in tcp_result["intervals"] if begin <= entry[key]["start"] < end]
                    assert len(intervals) >= 3 and all(entry["bytes"] > 0 for entry in intervals), f"TCP {key} stopped making progress during WAN outage at {begin}s"
            for guest, report in reports.items():
                assert report["loss_percent"] < 1, f"{guest} voice loss: {report}"
                assert report["max_gap_ms"] < 150, f"{guest} voice outage gap: {report}"
                assert report["p99_rtt_ms"] < 150, f"{guest} voice latency under load: {report}"
    finally:
        for guest in GUESTS:
            lab.execute(guest, "if test -f /root/voice.pid; then kill $(cat /root/voice.pid) 2>/dev/null || true; rm /root/voice.pid; fi")



if __name__ == "__main__":
    main()
