#!/usr/bin/env python3
"""Record observer wake delay and Linux CPU/thread scheduler counters."""

import argparse
import json
from pathlib import Path
import time


def scheduler(pids):
    records = {}
    for pid in pids:
        threads = {}
        for task in (Path("/proc") / str(pid) / "task").iterdir():
            try:
                fields = (task / "schedstat").read_text().split()
            except FileNotFoundError:
                continue  # Threads can exit between enumeration and observation.
            threads[task.name] = [int(value) for value in fields]
        if not threads:
            raise RuntimeError(f"observed process {pid} has no scheduler records")
        records[str(pid)] = threads
    return records


def record(output, seconds, pids):
    tick = 0.010
    start, next_sample = time.monotonic(), time.monotonic()
    deadline, worst, ticks = start + seconds, 0.0, 0
    with output.open("w") as stream:
        while time.monotonic() < deadline:
            time.sleep(max(0, next_sample - time.monotonic()))
            now = time.monotonic()
            worst = max(worst, now - next_sample)
            ticks += 1
            if ticks >= 10:
                cpu = [int(value) for value in Path("/proc/stat").read_text().splitlines()[0].split()[1:]]
                stream.write(json.dumps({"t": time.time(), "monotonic": now, "wake_delay_seconds": worst,
                    "cpu_ticks": cpu, "loadavg": Path("/proc/loadavg").read_text().strip(),
                    "threads": scheduler(pids)}) + "\n")
                stream.flush()
                worst, ticks = 0.0, 0
            next_sample += tick
            if next_sample < now:
                next_sample = now + tick


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seconds", type=float, required=True)
    parser.add_argument("--pid", type=int, action="append", required=True)
    args = parser.parse_args()
    if args.seconds <= 0 or any(pid <= 0 for pid in args.pid):
        parser.error("duration and process IDs must be positive")
    record(args.output, args.seconds, args.pid)


if __name__ == "__main__":
    main()
