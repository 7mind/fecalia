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


@dataclass(frozen=True)
class TimeBounds:
    earliest: float
    latest: float

    def __post_init__(self):
        if not math.isfinite(self.earliest) or not math.isfinite(self.latest) or self.latest < self.earliest:
            raise ValueError("invalid time bounds")

    def shifted(self, seconds):
        return TimeBounds(self.earliest + seconds, self.latest + seconds)


def event_bounds(event, clock_offsets, origin):
    if "changes" not in event:
        return None
    changes = event["changes"]
    if not changes:
        raise ValueError("impairment event has no changes")
    earliest = min(change["submitted_host"] for change in changes) - origin
    latest = max(change["at_guest"] - clock_offsets[change["guest"]][0] + clock_offsets[change["guest"]][1] for change in changes) - origin
    return TimeBounds(earliest, latest)


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


def bounded_latency(name, report, epoch, begin, end, clock_uncertainty, limit, fraction, strict):
    possible = voice_rtts(report, epoch, begin.earliest - clock_uncertainty, end.latest + clock_uncertainty)
    certain = voice_rtts(report, epoch, begin.latest + clock_uncertainty, end.earliest - clock_uncertainty)
    if len(certain) < 2:
        return unknown(name, "fewer than two voice packets certainly inside the required window")
    bad_possible = sum(value >= limit if strict else value > limit for value in possible)
    bad_certain = sum(value >= limit if strict else value > limit for value in certain)
    evidence = f"{len(certain)}..{len(possible)} samples; {bad_certain}..{bad_possible} exceed {'<' if strict else '<='}{limit:.3f} ms"
    # This bounds the quantile for every admissible placement of the boundary.
    if bad_possible <= len(certain) - 1 - int(len(certain) * fraction):
        return gate(name, True, evidence)
    if bad_certain > len(possible) - 1 - int(len(possible) * fraction):
        return gate(name, False, evidence)
    return unknown(name, evidence + "; boundary uncertainty changes the quantile verdict")


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
    early = [item for item in intervals if item.begin - origin_uncertainty >= begin and item.end + origin_uncertainty <= deadline.earliest] if deadline is not None else []
    steady = [item for item in intervals if item.begin - origin_uncertainty >= (begin if deadline is None else deadline.latest) and item.end + origin_uncertainty <= end]
    checks = []
    if deadline is None:
        pass
    elif not early or deadline.earliest - early[-1].end > early[-1].end - early[-1].begin:
        checks.append(unknown(name + "/deadline", "no complete receiver interval near deadline"))
    else:
        item = early[-1]
        if item.rate >= share * reference:
            checks.append(gate(name + "/deadline", True, f"[{item.begin:.3f},{item.end:.3f}) {item.rate:.0f} B/s; requires {share * reference:.0f}"))
        else:
            upper = sum(part.bytes for part in intervals if part.begin - origin_uncertainty < deadline.latest and part.end + origin_uncertainty > deadline.earliest - 1)
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


def covers(intervals, begin, end):
    reached = begin
    for item in sorted(intervals, key=lambda value: value.begin):
        if item.end <= reached:
            continue
        if item.begin > reached:
            return False
        reached = item.end
        if reached >= end:
            return True
    return False


def progress_gate(name, intervals, begin, end, origin_uncertainty):
    seconds = math.floor(end.latest - begin.earliest)
    if seconds <= 0:
        return unknown(name, "no full required second with bounded receiver observations")
    unresolved = 0
    for second in range(seconds):
        start = begin.shifted(second)
        finish = start.shifted(1)
        certain = [item for item in intervals if item.begin - origin_uncertainty >= start.latest
                   and item.end + origin_uncertainty <= min(finish.earliest, end.earliest)]
        if any(item.bytes > 0 for item in certain):
            continue
        possible = [item for item in intervals if item.begin - origin_uncertainty < finish.latest
                    and item.end + origin_uncertainty > start.earliest]
        if finish.latest <= end.earliest and possible and all(item.bytes == 0 for item in possible) and covers(possible, start.earliest - origin_uncertainty, finish.latest + origin_uncertainty):
            return gate(name, False, f"no receiver delivery in required second {second + 1}")
        unresolved += 1
    evidence = f"{seconds - unresolved}/{seconds} required seconds have certain positive receiver delivery"
    return unknown(name, evidence + "; missing intervals or timestamp resolution leave progress unresolved") if unresolved else gate(name, True, evidence)


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
    evidence = f"longest measured cut >25%: {longest:.3f}s; pre-change {statistics.mean(before):.0f} B/s"
    if longest > 2:
        return gate(name, False, evidence)
    return gate(name, True, evidence) if covers(intervals, at, end) else unknown(name, evidence + "; receiver intervals do not cover the change window")


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


def receipt_gate(name, samples, begin, end, lane):
    if not samples or any("bounds" not in sample for sample in samples):
        return unknown(name, "missing metric-read completion bounds")
    measured = [(sample["bounds"], metric(sample, "wanbond_adaptive_received_bulk_packets_total", lane)) for sample in samples]
    relevant = [(bounds, count) for bounds, count in measured if bounds.latest >= begin.earliest and bounds.earliest <= end.latest]
    before = [(bounds, count) for bounds, count in measured if bounds.latest <= begin.earliest]
    after = [(bounds, count) for bounds, count in measured if bounds.earliest >= end.latest]
    covered = ([before[-1]] if before else []) + relevant + ([after[0]] if after else [])
    if len(covered) < 2 or any(count is None for _, count in covered):
        return unknown(name, "missing physical bulk-receipt counter coverage")
    if any(right < left for (_, left), (_, right) in zip(covered, covered[1:])):
        return unknown(name, "physical bulk-receipt counter reset during measurement")
    inside = [(bounds, count) for bounds, count in covered if bounds.earliest >= begin.latest and bounds.latest <= end.earliest]
    if len(inside) >= 2 and inside[-1][1] > inside[0][1]:
        return gate(name, True, f"lane {lane} received {inside[-1][1] - inside[0][1]:g} physical bulk attempts certainly within the deadline")
    if before and after and covered[-1][1] == covered[0][1]:
        return gate(name, False, f"lane {lane} received no physical bulk attempts across the whole bounded deadline window")
    return unknown(name, "counter intervals cannot place a physical bulk receipt inside the deadline")


@dataclass(frozen=True)
class Egress:
    offered: int
    accepted: int
    identity: tuple


def egress(sample, device):
    if "tc" not in sample or device not in sample["tc"]:
        return None
    tc = sample["tc"][device]
    roots = [item for item in tc["qdisc"] if item.get("root")]
    police = [action for rule in tc["filter"] for action in rule.get("options", {}).get("actions", []) if action["kind"] == "police"]
    if len(roots) != 1 or roots[0]["kind"] != "htb" or len(police) != 1:
        return None
    root, action = roots[0], police[0]
    return Egress(action["stats"]["bytes"], root["bytes"] + root["backlog"], (root["handle"], action["index"]))


def egress_loss_gate(name, samples, begin, end, device):
    if not samples or any("bounds" not in sample for sample in samples):
        return unknown(name, "missing egress-read completion bounds")
    before = [sample for sample in samples if sample["bounds"].latest <= begin.earliest]
    after = [sample for sample in samples if sample["bounds"].earliest >= end.latest]
    if not before or not after:
        return unknown(name, "egress counters do not cover the whole bounded phase")
    covered = [before[-1]] + [sample for sample in samples if sample["bounds"].latest >= begin.earliest and sample["bounds"].earliest <= end.latest] + [after[0]]
    counters = [egress(sample, device) for sample in covered]
    if any(value is None for value in counters):
        return unknown(name, "missing root HTB and policer byte counters")
    if len({value.identity for value in counters}) != 1 or any(right.offered < left.offered or right.accepted < left.accepted for left, right in zip(counters, counters[1:])):
        return unknown(name, "egress counters reset or changed identity during measurement")
    inside = [value for sample, value in zip(covered, counters) if sample["bounds"].earliest >= begin.latest and sample["bounds"].latest <= end.earliest]
    if len(inside) < 2 or inside[-1].offered == inside[0].offered:
        return unknown(name, "no traffic certainly inside the loss measurement window")
    offered_lower, offered_upper = inside[-1].offered - inside[0].offered, counters[-1].offered - counters[0].offered
    accepted_lower, accepted_upper = inside[-1].accepted - inside[0].accepted, counters[-1].accepted - counters[0].accepted
    if accepted_lower > offered_upper:
        raise ValueError("egress byte conservation violated")
    # Backlog belongs to accepted bytes: draining an old queue is not new delivery.
    lower = max(0, offered_lower - accepted_upper) / offered_upper
    upper = (offered_upper - accepted_lower) / offered_lower
    evidence = f"{device} byte-loss bounds {100 * lower:.3f}..{100 * upper:.3f}%; offered {offered_lower}..{offered_upper} bytes"
    if upper < .05:
        return gate(name, True, evidence)
    if lower >= .05:
        return gate(name, False, evidence)
    return unknown(name, evidence + "; boundary uncertainty changes the verdict")


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
            if "t_complete" in sample:
                uncertainty = manifest["clock_offsets"][guest][1]
                sample["bounds"] = TimeBounds(sample["at"] - uncertainty, sample["t_complete"] - offsets[guest] - origin + uncertainty)
    tcp, tcp_uncertainty, tcp_starts = {}, {}, {}
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
            if "test_started_guest" in data:
                uncertainty = manifest["clock_offsets"][guest][1]
                tcp_starts[guest] = TimeBounds(data["start"]["timestamp"]["timemillisecs"] / 1000 - offsets[guest] - origin - uncertainty,
                                              data["test_started_guest"] - offsets[guest] - origin + uncertainty)
    events = manifest["events"]
    bounds = [TimeBounds(0, 0)] + [event_bounds(event, manifest["clock_offsets"], origin) for event in events] + [TimeBounds(manifest["seconds"], manifest["seconds"])]
    case = manifest["scenario"]
    checks = []
    for guest, report in voices.items():
        if case.startswith(("blackout", "oneway", "both-latency")):
            checks.extend(voice_continuity(guest + "/run", report, case.startswith("both-latency")))
    phases = references.get("phases", {})
    for phase in range(len(bounds) - 1):
        start_bounds, end_bounds = bounds[phase:phase + 2]
        reference = phases.get(str(phase), {})
        event = events[phase - 1]["name"] if phase else "start"
        row, deadline = None, None
        if case == "cold":
            row, deadline = "0", 7
            start_bounds = TimeBounds(min(value.earliest for value in tcp_starts.values()), max(value.latest for value in tcp_starts.values())) if len(tcp_starts) == 2 else None
        elif case.startswith(("blackout", "oneway")) and phase:
            row, deadline = ("1c", 3) if case.startswith("oneway") and "dark" in event else (("1a", 3) if "dark" in event else ("1b", 5))
        elif case.startswith("rate-") and phase:
            row, deadline = "2a", 5
        elif case == "rise" and phase:
            row, deadline = "2b", 10
        elif case == "plan" and phase:
            row, deadline = "2c", 20 if phase == 1 else None
        elif case == "cellular":
            row, deadline, start_bounds = "2d", None, TimeBounds(20, 20)
        elif case.startswith("latency") and phase in (1, 3):
            row = "3a" if phase == 1 else "3b"
        elif case.startswith("both-latency") and phase:
            row = "3c"
        if row is None:
            continue
        if start_bounds is None or end_bounds is None:
            checks.append(unknown(f"{row}/phase{phase}/timing", "missing impairment submission/application bounds"))
            continue
        begin, end = start_bounds.latest, end_bounds.earliest
        if begin >= end:
            checks.append(unknown(f"{row}/phase{phase}/timing", "impairment bounds overlap the next phase"))
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
                        lag = 2 if row == "3a" else 1 if row != "1b" else 0
                        checks.append(bounded_latency(name + "/latency", report, epoch, start_bounds.shifted(lag), end_bounds, manifest["clock_offsets"][guest][1], limit[side] + (20 if row == "3a" else 50), .5 if row == "3a" else .99, False))
                        if row == "3a":
                            checks.append(bounded_latency(name + "/latency-deadline", report, epoch, start_bounds.shifted(1), start_bounds.shifted(2), manifest["clock_offsets"][guest][1], limit[side] + 20, .5, False))
                elif row in ("2a", "2c", "2d"):
                    checks.append(bounded_latency(name + "/latency", report, epoch, start_bounds.shifted(-2) if row == "2a" else start_bounds, end_bounds, manifest["clock_offsets"][guest][1], 150, .99, True))
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
                    deadline_bounds = None if deadline is None else start_bounds.shifted(deadline)
                    checks.extend(bulk_gate(name + "/bulk", tcp[guest], begin, end, deadline_bounds, None if rates is None else rates[side], .6 if row == "0" else .7 if row == "2d" else .75, row != "0", tcp_uncertainty[guest]))
                if row in ("1a", "1c"):
                    checks.append(progress_gate(name + "/progress", tcp[guest], start_bounds.shifted(1), end_bounds, tcp_uncertainty[guest]))
                if row in ("3a", "3c"):
                    checks.append(bulk_reduction(name + "/bulk", tcp[guest], begin, end))
                if row == "3b":
                    checks.append(unknown(name + "/bulk", "definition of unchanged bulk pending"))
                if row == "2a":
                    checks.append(unknown(name + "/expiry", "definition of an expired-datagram burst pending"))
                if row == "1b":
                    returned = {change["lane"] for change in events[phase - 1]["changes"]}
                    if len(returned) != 1 or not returned.issubset({1, 2}):
                        checks.append(unknown(name + "/lane-bulk", "return event does not identify one physical WAN"))
                    else:
                        lane = "0" if returned == {1} else "1" if guest == "hub" else "256"
                        checks.append(receipt_gate(name + "/lane-bulk", samples[guest], start_bounds, start_bounds.shifted(2), lane))
                if row == "2c" and phase == 2:
                    checks.append(egress_loss_gate(name + "/lane-loss", samples[guest], start_bounds.shifted(3), end_bounds, "eth1"))
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
