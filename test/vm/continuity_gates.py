#!/usr/bin/env python3
"""Pass criteria of the continuity scenario, evaluated on a recorded run."""

import argparse
import json
from pathlib import Path

GUESTS = ("hub", "edge")
MAX_LOSS_PERCENT = 1
# A cold start has no capacity estimate yet; its latency is not gated.
COLD_START_SECONDS = 5
COLD_START_GAP_MS = 200
MAX_GAP_MS = 150
P99_RTT_MS = 150
# Queueing allowed on top of what the only remaining WAN does when idle.
QUEUEING_ALLOWANCE_MS = 50
TCP_PROGRESS_WINDOW = 3
QUANTILE_STEPS = 200


def p99(values):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(len(ordered) * 0.99))]


def unloaded_p99(forward, back):
    """p99 round trip of an idle WAN whose one-way delays are uniform in delay±jitter."""
    def one_way(condition):
        return [condition["delay"] + condition["jitter"] * (2 * (step + 0.5) / QUANTILE_STEPS - 1) for step in range(QUANTILE_STEPS)]
    return p99([there + here for there in one_way(forward) for here in one_way(back)])


def segments(scenario):
    """(begin, end, lanes up) between the recorded changes of WAN conditions."""
    up = {int(lane) for lane in scenario["profile"]["hub"]}
    begin, out = 0.0, []
    for phase in scenario["phases"]:
        out.append((begin, phase["at_seconds"], frozenset(up)))
        begin = phase["at_seconds"]
        down = all(condition["loss"] >= 100 for condition in phase["conditions"].values())
        (up.discard if down else up.add)(phase["lane"])
    out.append((begin, float("inf"), frozenset(up)))
    return out


def voice_failures(guest, report, scenario):
    failures = []
    if report["loss_percent"] >= MAX_LOSS_PERCENT:
        failures.append(f"{guest} voice loss {report['loss_percent']:.2f}% (requires <{MAX_LOSS_PERCENT}%)")
    samples = report["samples"]
    arrivals = [0.0] + [sample["at"] for sample in samples]
    for before, after in zip(arrivals, arrivals[1:]):
        limit = COLD_START_GAP_MS if after < COLD_START_SECONDS else MAX_GAP_MS
        if (after - before) * 1000 >= limit:
            failures.append(f"{guest} voice gap {(after - before) * 1000:.0f} ms ending at {after:.1f} s (requires <{limit} ms)")
    profile = scenario["profile"]
    lanes = sorted(profile["hub"], key=lambda lane: unloaded_p99(profile["hub"][lane], profile["edge"][lane]))
    fastest = int(lanes[0])
    groups = {}
    for begin, end, up in segments(scenario):
        begin = max(begin, COLD_START_SECONDS)
        if fastest in up or not up:
            name, limit = "lowest-latency WAN up", P99_RTT_MS
        else:
            idle = max(unloaded_p99(profile["hub"][str(lane)], profile["edge"][str(lane)]) for lane in up)
            name, limit = f"only WAN {sorted(up)} up", max(P99_RTT_MS, idle + QUEUEING_ALLOWANCE_MS)
        # A datagram belongs to the conditions it was sent under.
        groups.setdefault((name, limit), []).extend(
            sample["rtt_ms"] for sample in samples if begin <= sample["at"] - sample["rtt_ms"] / 1000 < end)
    for (name, limit), delays in groups.items():
        if delays and p99(delays) >= limit:
            failures.append(f"{guest} voice p99 RTT {p99(delays):.1f} ms with {name} (requires <{limit:.1f} ms)")
    return failures


def tcp_failures(tcp, scenario):
    failures = []
    outages = []
    down = {}
    for phase in scenario["phases"]:
        lost = all(condition["loss"] >= 100 for condition in phase["conditions"].values())
        if lost:
            down[phase["lane"]] = phase["at_seconds"]
        elif phase["lane"] in down:
            outages.append((down.pop(phase["lane"]), phase["at_seconds"]))
    for receiver, report in (("edge", tcp), ("hub", tcp["server_output_json"])):
        # Check delivery at each receiver; completed writes can be absent for
        # a whole interval while the kernel still transmits.
        received = [entry[key] for entry in report["intervals"]
                    for key in ("sum", "sum_bidir_reverse") if not entry[key]["sender"]]
        for begin, end in outages:
            inside = [entry for entry in received if begin <= entry["start"] and entry["end"] <= end]
            if len(inside) < TCP_PROGRESS_WINDOW:
                failures.append(f"TCP receiver {receiver} reported {len(inside)} intervals in the outage at {begin:.0f} s")
            for first in range(len(inside) - TCP_PROGRESS_WINDOW + 1):
                window = inside[first:first + TCP_PROGRESS_WINDOW]
                if not any(entry["bytes"] > 0 for entry in window):
                    failures.append(f"TCP receiver {receiver} made no progress for {TCP_PROGRESS_WINDOW} s from {window[0]['start']:.0f} s")
            # The first whole interval after recovery ends at most two seconds after it.
            after = [entry for entry in received if entry["start"] >= end][:1]
            if not after or after[0]["bytes"] == 0:
                failures.append(f"TCP receiver {receiver} had not resumed after the recovery at {end:.0f} s")
    return failures


def evaluate(directory):
    scenario = json.loads((directory / "scenario.json").read_text())
    tcp = json.loads((directory / "tcp.json").read_text())
    if "error" in tcp:
        return [f"TCP: {tcp['error']}"]
    failures = []
    if len(tcp["intervals"]) < 60:
        failures.append("TCP did not survive the complete scenario")
    failures += tcp_failures(tcp, scenario)
    for guest in GUESTS:
        failures += voice_failures(guest, json.loads((directory / f"{guest}-voice.json").read_text()), scenario)
    return failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, nargs="+")
    args = parser.parse_args()
    failed = False
    for directory in args.directory:
        failures = evaluate(directory)
        failed = failed or bool(failures)
        print(directory.name, "pass" if not failures else "FAIL")
        for failure in failures:
            print("  " + failure)
    raise SystemExit(1 if failed else 0)


if __name__ == "__main__":
    main()
