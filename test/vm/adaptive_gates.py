"""Section 4 gates over receiver evidence; missing references are inconclusive."""

import argparse
from dataclasses import asdict, dataclass
import json
import math
from pathlib import Path
import statistics


@dataclass(frozen=True)
class Check:
    name: str
    status: str
    evidence: str


@dataclass(frozen=True)
class Interval:
    begin: float
    end: float
    bytes: int

    @property
    def rate(self):
        return self.bytes / (self.end - self.begin)


def gate(name, satisfied, evidence):
    return Check(name, "pass" if satisfied else "fail", evidence)


def unknown(name, reason):
    return Check(name, "inconclusive", reason)


def quantile(values, fraction):
    if not values or any(not math.isfinite(value) for value in values):
        raise ValueError("quantile requires finite nonempty measurements")
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(len(ordered) * fraction))]


def voice_continuity(name, report, zero_loss):
    sent = report["sent"]
    samples = report["samples"]
    received = {int(sample[0]) for sample in samples}
    if sent <= 0 or len(samples) < 2 or len(received) != len(samples) or any(seq < 0 or seq >= sent for seq in received):
        raise ValueError("voice continuity requires unique receipts in the sent sequence range")
    consecutive = worst = 0
    for seq in range(sent):
        consecutive = 0 if seq in received else consecutive + 1
        worst = max(worst, consecutive)
    arrivals = sorted(sample[1] for sample in samples)
    gap_ms = max(b - a for a, b in zip(arrivals, arrivals[1:])) * 1000
    lost = sent - len(received)
    return [gate(name + "/loss", lost == 0 if zero_loss else lost * 100 < sent, f"lost {lost}/{sent}"),
            gate(name + "/consecutive", worst <= 3, f"maximum {worst}"),
            gate(name + "/gap", gap_ms < 150, f"maximum {gap_ms:.3f} ms; limit <150")]


def voice_rtts(report, epoch, begin, end):
    # An echoed packet carries its actual send stamp; lost packets do not.
    return [rtt for _, arrival, rtt in report["samples"] if begin <= epoch + arrival - rtt / 1000 < end]


def latency(name, report, epoch, begin, end, limit, fraction, strict):
    values = voice_rtts(report, epoch, begin, end)
    if len(values) < 2:
        return unknown(name, "fewer than two received voice packets in measurement window")
    actual = quantile(values, fraction)
    return gate(name, actual < limit if strict else actual <= limit, f"q{fraction * 100:g}={actual:.3f} ms; limit {'<' if strict else '<='}{limit:.3f}")


def receiver_intervals(data, clock_offset, origin):
    if "test_started_guest" not in data:
        raise ValueError("receiver clock needs the actual iperf test-start event")
    epoch = data["test_started_guest"] - clock_offset - origin
    result = []
    for interval in data["intervals"]:
        receivers = [stream for stream in interval["streams"] if stream["sender"] is False and not stream["omitted"]]
        if len(receivers) != 1:
            raise ValueError("one TCP receiver stream required per interval")
        stream = receivers[0]
        if stream["end"] <= stream["start"] or stream["bytes"] < 0:
            raise ValueError("invalid TCP receiver interval")
        result.append(Interval(epoch + stream["start"], epoch + stream["end"], stream["bytes"]))
    return result


def bulk_gate(name, intervals, begin, end, deadline, reference, share, sustain, origin_uncertainty):
    if not math.isfinite(origin_uncertainty) or origin_uncertainty < 0:
        raise ValueError("receiver-origin uncertainty must be finite and nonnegative")
    if reference is None or not math.isfinite(reference) or reference <= 0:
        return [unknown(name, "missing successful independent phase calibration")]
    # Use complete receiver intervals, without inventing within-interval arrival times.
    early = [item for item in intervals if item.begin - origin_uncertainty >= begin and item.end + origin_uncertainty <= deadline] if deadline is not None else []
    steady = [item for item in intervals if item.begin - origin_uncertainty >= (begin if deadline is None else deadline) and item.end + origin_uncertainty <= end]
    checks = []
    if deadline is None:
        pass
    elif not early or deadline - early[-1].end > early[-1].end - early[-1].begin:
        checks.append(unknown(name + "/deadline", "no complete receiver interval near deadline"))
    else:
        item = early[-1]
        if item.rate >= share * reference:
            checks.append(gate(name + "/deadline", True, f"[{item.begin:.3f},{item.end:.3f}) {item.rate:.0f} B/s; requires {share * reference:.0f}"))
        else:
            upper = sum(part.bytes for part in intervals if part.begin - origin_uncertainty < deadline and part.end + origin_uncertainty > deadline - 1)
            checks.append(gate(name + "/deadline", False, f"final-second upper bound {upper} bytes < required {share * reference:.0f}")
                          if upper < share * reference else unknown(name + "/deadline", "partial intervals and start-time uncertainty cannot resolve deadline"))
    if not sustain:
        return checks
    if not steady:
        checks.append(unknown(name + "/steady", "no complete receiver intervals after deadline"))
    else:
        rate = sum(item.bytes for item in steady) / sum(item.end - item.begin for item in steady)
        checks.append(gate(name + "/steady", rate >= share * reference, f"{rate:.0f} B/s; requires {share * reference:.0f}"))
    return checks


def bulk_reduction(name, intervals, at, end):
    before = [item.rate for item in intervals if item.begin >= at - 5 and item.end <= at]
    after = [item for item in intervals if item.begin >= at and item.end <= end]
    if not before or not after or statistics.mean(before) <= 0:
        return unknown(name, "missing nonzero pre-change TCP receiver reference")
    limit = .75 * statistics.mean(before)
    run = longest = 0.0
    for item in after:
        run = run + item.end - item.begin if item.rate < limit else 0.0
        longest = max(longest, run)
    return gate(name, longest <= 2, f"longest measured cut >25%: {longest:.3f}s; pre-change {statistics.mean(before):.0f} B/s")


def metric(sample, name, lane):
    values = [value for key, value in sample["m"].items() if key.startswith(name + "{") and (lane is None or f'lane="{lane}"' in key)]
    if len(values) != 1:
        return None
    return values[0]


def route_gate(name, samples, begin, end, lanes, desired):
    chosen = [sample for sample in samples if begin <= sample["at"] < end]
    if len(chosen) < 2:
        return unknown(name, "missing route sample coverage")
    deltas = []
    for lane in lanes:
        first = metric(chosen[0], "wanbond_adaptive_realtime_original_packets_total", lane)
        last = metric(chosen[-1], "wanbond_adaptive_realtime_original_packets_total", lane)
        if first is None or last is None:
            return unknown(name, "binary does not export first-submission route counters")
        deltas.append(last - first)
    return gate(name, deltas[lanes.index(desired)] > 0 and deltas[1 - lanes.index(desired)] == 0,
                f"primary submissions by lane {dict(zip(lanes, deltas))}")


def evaluate(directory, references):
    directory = Path(directory)
    manifest = json.loads((directory / "scenario.json").read_text())
    origin = manifest["start_host"]
    offsets = {guest: manifest["clock_offsets"][guest][0] for guest in ("edge", "hub")}
    voices, samples = {}, {}
    for guest in ("edge", "hub"):
        if manifest["voice"]:
            voices[guest] = json.loads((directory / f"{guest}-voice.json").read_text())
        samples[guest] = [json.loads(line) for line in (directory / f"{guest}-samples.jsonl").read_text().splitlines()]
        for sample in samples[guest]:
            sample["at"] = sample["t"] - offsets[guest] - origin
    tcp, tcp_uncertainty = {}, {}
    if manifest["tcp"]:
        report = json.loads((directory / "tcp.json").read_text())
        server = report["server_output_json"]
        server_path = directory / "hub-tcp.json"
        if server_path.exists():
            server = json.loads(server_path.read_text())
        tcp["edge"] = receiver_intervals(report, offsets["edge"], origin) if "test_started_guest" in report else []
        tcp["hub"] = receiver_intervals(server, offsets["hub"], origin) if "test_started_guest" in server else []
        for guest, data in (("edge", report), ("hub", server)):
            tcp_uncertainty[guest] = max(0, data["test_started_guest"] - data["start"]["timestamp"]["timemillisecs"] / 1000) + 2 * manifest["clock_offsets"][guest][1] if "test_started_guest" in data else 0
    events = manifest["events"]
    bounds = [0] + [max(event["at_host"], *(at - offsets[guest] for guest, at in event["at_guest"].items())) - origin for event in events] + [manifest["seconds"]]
    case = manifest["scenario"]
    checks = []
    for guest, report in voices.items():
        if case.startswith(("blackout", "oneway", "both-latency")):
            checks.extend(voice_continuity(guest + "/run", report, case.startswith("both-latency")))
    phases = references.get("phases", {})
    for phase in range(len(bounds) - 1):
        begin, end = bounds[phase:phase + 2]
        reference = phases.get(str(phase), {})
        event = events[phase - 1]["name"] if phase else "start"
        row, deadline = None, None
        if case == "cold":
            row, deadline = "0", 7
        elif case.startswith(("blackout", "oneway")) and phase:
            row, deadline = ("1c", 3) if case.startswith("oneway") and "dark" in event else (("1a", 3) if "dark" in event else ("1b", 5))
        elif case.startswith("rate-") and phase:
            row, deadline = "2a", 5
        elif case == "rise" and phase:
            row, deadline = "2b", 10
        elif case == "plan" and phase:
            row, deadline = "2c", 20 if phase == 1 else None
        elif case == "cellular":
            row, deadline, begin = "2d", None, 20
        elif case.startswith("latency") and phase in (1, 3):
            row = "3a" if phase == 1 else "3b"
        elif case.startswith("both-latency") and phase:
            row = "3c"
        if row is None:
            continue
        for side, guest in enumerate(("edge", "hub")):
            name = f"{row}/phase{phase}/{guest}"
            if guest in voices:
                report = voices[guest]
                epoch = report["epoch"] - offsets[guest] - origin
                if row in ("1a", "1b", "1c", "3a"):
                    key = "idle_p50_ms" if row == "3a" else "idle_p99_ms"
                    limit = reference.get(key)
                    if limit is None:
                        checks.append(unknown(name + "/latency", "missing independent idle latency reference"))
                    else:
                        checks.append(latency(name + "/latency", report, epoch, begin + (2 if row == "3a" else 1 if row != "1b" else 0), end, limit[side] + (20 if row == "3a" else 50), .5 if row == "3a" else .99, False))
                elif row in ("2a", "2c", "2d"):
                    checks.append(latency(name + "/latency", report, epoch, max(0, begin - 2) if row == "2a" else begin, end, 150, .99, True))
                elif row == "2b":
                    checks.append(unknown(name + "/latency", "definition of unchanged latency pending"))
                if row in ("2c", "3b"):
                    desired = "0" if row == "2c" else "1" if guest == "hub" else "256"
                    lanes = ("0", "1") if guest == "hub" else ("0", "256")
                    checks.append(route_gate(name + "/route", samples[guest], begin + (5 if row == "3b" else 0), end, lanes, desired))
                    if row == "3b":
                        checks.append(unknown(name + "/move-spacing", "five-second spacing needs counter increments with bounded sample times"))
            if guest in tcp:
                if deadline is not None or row == "2d":
                    rates = reference.get("payload_bps")
                    if row == "0":
                        sender = "hub" if guest == "edge" else "edge"
                        rates = [sum(lane["rate"] for lane in manifest["profile"][sender].values()) * 1e6 / 8] * 2
                    # Client receiver is downlink; server receiver is uplink.
                    checks.extend(bulk_gate(name + "/bulk", tcp[guest], begin, end, None if deadline is None else begin + deadline, None if rates is None else rates[side], .6 if row == "0" else .7 if row == "2d" else .75, row != "0", tcp_uncertainty[guest]))
                if row in ("1a", "1c"):
                    intervals = [item for item in tcp[guest] if item.begin >= begin + 1 and item.end <= end]
                    checks.append(gate(name + "/progress", all(item.bytes > 0 for item in intervals), f"{len(intervals)} complete receiver intervals") if intervals else unknown(name + "/progress", "missing time-aligned TCP receiver intervals"))
                if row in ("3a", "3c"):
                    checks.append(bulk_reduction(name + "/bulk", tcp[guest], begin, end))
                if row == "3b":
                    checks.append(unknown(name + "/bulk", "definition of unchanged bulk pending"))
                if row == "2a":
                    checks.append(unknown(name + "/expiry", "definition of an expired-datagram burst pending"))
                if row == "1b":
                    checks.append(unknown(name + "/lane-bulk", "aggregate lane bytes include small TCP, copies and repairs"))
                if row == "2c" and phase == 2:
                    checks.append(unknown(name + "/lane-loss", "physical loss after three seconds needs phase-bounded qdisc/filter counters"))
    return checks


def write_report(directory, references):
    checks = evaluate(directory, references)
    report = {"status": "fail" if any(check.status == "fail" for check in checks) else "inconclusive" if not checks or any(check.status == "inconclusive" for check in checks) else "pass", "checks": [asdict(check) for check in checks]}
    (directory / "gates.json").write_text(json.dumps(report, indent=2))
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "pass" else 1 if report["status"] == "fail" else 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--references", type=Path)
    args = parser.parse_args()
    references = json.loads(args.references.read_text()) if args.references is not None else {}
    raise SystemExit(write_report(args.directory, references))


if __name__ == "__main__":
    main()
