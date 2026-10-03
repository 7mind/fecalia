"""Retain iperf JSON while recording its actual test-start event in the guest."""

import argparse
from collections.abc import Callable, Iterable
import json
import math
from pathlib import Path
import subprocess
import sys
import time


def capture(lines: Iterable[str], clock: Callable[[], float], record: Callable[[float, str], None]):
    report = {"intervals": []}
    started = False
    ended = False
    for line in lines:
        observed = clock()
        event = json.loads(line)
        name, data = event["event"], event["data"]
        if not math.isfinite(observed):
            raise ValueError("nonfinite event clock")
        record(observed, name)
        if name == "error":
            raise RuntimeError(f"iperf error: {data}")
        if name == "start":
            if started:
                raise ValueError("duplicate test-start event")
            report["start"], report["test_started_guest"] = data, observed
            started = True
        elif name == "interval":
            if not started or ended:
                raise ValueError("interval outside started test")
            report["intervals"].append(data)
        elif name == "end":
            if not started or ended:
                raise ValueError("end outside started test")
            report["end"] = data
            ended = True
        elif name == "server_output_json":
            report["server_output_json"] = data
        elif name != "server_output_text":
            raise ValueError(f"unknown iperf event: {name}")
    if not ended or not report["intervals"]:
        raise ValueError("incomplete iperf test")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timing", type=Path, required=True)
    parser.add_argument("--pid-file", type=Path, required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("an iperf command is required")
    child = subprocess.Popen(command + ["--json-stream", "--forceflush"], stdout=subprocess.PIPE, text=True)
    args.pid_file.write_text(str(child.pid))
    try:
        with args.timing.open("w") as timing:
            def record(at, event):
                timing.write(json.dumps({"guest_time": at, "event": event}) + "\n")
                timing.flush()
            report = capture(child.stdout, time.time, record)
        status = child.wait()
        if status:
            raise RuntimeError(f"iperf exited with status {status}")
        print(json.dumps(report))
    finally:
        if child.poll() is None:
            child.terminate()
        child.wait()
        args.pid_file.unlink()


if __name__ == "__main__":
    main()
