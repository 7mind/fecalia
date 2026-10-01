#!/usr/bin/env python3
"""A call in both directions while one side offers UDP far above the bond's capacity."""

import argparse
import concurrent.futures
import json
import time
from pathlib import Path

from benchmark import apply_profile, profile_from, provision
from lab import GUESTS, Lab


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--profile", type=Path, default=Path(__file__).with_name("profiles") / "fast.json")
    parser.add_argument("--mbit", type=int, default=400, help="offered UDP rate")
    parser.add_argument("--seconds", type=int, default=20)
    parser.add_argument("--reverse", action="store_true", help="flood from the hub to the edge")
    args = parser.parse_args()
    lab = Lab()
    lab.acquire()
    apply_profile(lab, profile_from(args.profile))
    provision(lab, args.binary)
    for guest in GUESTS:
        address = "10.77.0.1" if guest == "hub" else "10.77.0.2"
        lab.put(guest, Path(__file__).with_name("voice.py"), "/root/voice.py")
        lab.execute(guest, f"nohup python3 /root/voice.py server {address} > /root/voice-server.log 2>&1 < /dev/null & echo $! > /root/voice.pid")
    lab.execute("hub", "iperf3 -s -1 -D -B 10.77.0.1")
    try:
        with concurrent.futures.ThreadPoolExecutor() as pool:
            voices = {guest: pool.submit(lab.execute, guest, f"python3 /root/voice.py client 10.77.0.{2 if guest == 'hub' else 1} --seconds {args.seconds + 6}", capture_output=True) for guest in GUESTS}
            time.sleep(3)
            udp = json.loads(lab.execute("edge", f"iperf3 -c 10.77.0.1 -u -b {args.mbit}M -l 1300 -t {args.seconds} -J {'-R' if args.reverse else ''}", capture_output=True).stdout)
            flow = udp["end"]["sum"]
            result = {"offered_mbit_s": args.mbit, "delivered_mbit_s": flow["bits_per_second"] * (1 - flow["lost_percent"] / 100) / 1e6}
            for guest, future in voices.items():
                report = json.loads(future.result().stdout)
                result[guest] = {key: value for key, value in report.items() if key != "samples"}
    finally:
        for guest in GUESTS:
            lab.execute(guest, "if test -f /root/voice.pid; then kill $(cat /root/voice.pid) 2>/dev/null || true; rm /root/voice.pid; fi")
    print(json.dumps(result), flush=True)
    for guest in GUESTS:
        assert result[guest]["loss_percent"] < 1, f"{guest} voice loss beside a flood requires <1%"


if __name__ == "__main__":
    main()
