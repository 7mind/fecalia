#!/usr/bin/env python3
"""Measure each emulated WAN without wanbond, in both directions."""

import argparse
import concurrent.futures
import json
from pathlib import Path
import time

from lab import Lab
from benchmark import apply_profile, profile_from


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profiles") / "basic.json")
    args = parser.parse_args()
    lab = Lab()
    lab.acquire()
    profile = profile_from(args.profile)
    apply_profile(lab, profile)
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-calibration")
    output.mkdir()
    summary = {"profile": profile, "measurements": {}}
    for protocol in ("tcp", "udp"):
        for reverse in (False, True):
            direction = "downlink" if reverse else "uplink"
            sender = "hub" if reverse else "edge"
            for lane in (1, 2):
                lab.execute("hub", f"iperf3 -s -1 -D -B 10.77.200.1 -p {5210+lane}")
            with concurrent.futures.ThreadPoolExecutor() as pool:
                tests = {}
                for lane in (1, 2):
                    rate = profile[sender][str(lane)]["rate"]
                    flags = f"-u -b {rate}M -l 1300" if protocol == "udp" else ""
                    tests[lane] = pool.submit(lab.execute, "edge", f"iperf3 -c 10.77.200.1 -B 10.77.{lane}.2 -p {5210+lane} -t 10 -O 3 -J {flags} {'-R' if reverse else ''}", capture_output=True)
                for lane, future in tests.items():
                    data = json.loads(future.result().stdout)
                    name = f"{protocol}-{direction}-wan{lane}"
                    (output / f"{name}.json").write_text(json.dumps(data, indent=2))
                    summary["measurements"][name] = data["end"]["sum_received"]["bits_per_second"] / 1e6
    (output / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps(summary, indent=2))
    print(output)
    for direction, sender in (("uplink", "edge"), ("downlink", "hub")):
        for lane, condition in profile[sender].items():
            for protocol in ("tcp", "udp"):
                actual = summary["measurements"][f"{protocol}-{direction}-wan{lane}"]
                assert actual >= condition["rate"] * 0.85, "emulator failed capacity calibration; tunnel performance result is inconclusive"


if __name__ == "__main__":
    main()
