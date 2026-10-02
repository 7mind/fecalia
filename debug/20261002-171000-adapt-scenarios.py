#!/usr/bin/env python3
"""Baseline of how the transport follows link changes: a blackout, a rate step, a latency step.

Runs the actual daemons in the KVM lab (test/vm) under voice in both directions and,
unless the scenario says otherwise, one TCP flow in each direction, changes the WANs on
a timeline and records, ten times a second in each guest, the interface counters and the
transport's lane series. Throughput is read from the tunnel interface's byte counters,
not from iperf3: its JSON has no sub-second time base.

  python3 debug/20261002-171000-adapt-scenarios.py run result/bin/wanbond blackout
  python3 debug/20261002-171000-adapt-scenarios.py show <result directory>
"""

import argparse
import concurrent.futures
import hashlib
import json
import math
from pathlib import Path
import statistics
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "test" / "vm"))
from lab import GUESTS, Lab  # noqa: E402
from benchmark import provision  # noqa: E402

VOICE = r'''
import json, select, socket, struct, sys, time
mode, address = sys.argv[1], sys.argv[2]
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
if mode == "server":
    sock.bind((address, 5202))
    while True:
        data, source = sock.recvfrom(2048)
        sock.sendto(data, source)
seconds = float(sys.argv[3])
sock.connect((address, 5202))
sock.setblocking(False)
epoch, start = time.time(), time.monotonic()
next_send, sent, samples, seen, end = start, 0, [], set(), start + seconds
while time.monotonic() < end + 1:
    now = time.monotonic()
    if now >= next_send and now < end:
        sock.send(struct.pack("!Qd", sent, now) + bytes(144))
        sent += 1
        next_send += 0.020
    readable, _, _ = select.select([sock], [], [], max(0, min(0.020, next_send - now)) if now < end else 0.020)
    if readable:
        try:
            packet = sock.recv(2048)
        except OSError:
            continue
        received = time.monotonic()
        seq, stamp = struct.unpack("!Qd", packet[:16])
        if seq in seen:
            continue
        seen.add(seq)
        samples.append([seq, received - start, (received - stamp) * 1000])
print(json.dumps({"epoch": epoch, "sent": sent, "samples": samples}))
'''

SAMPLER = r'''
import json, sys, time, urllib.request
keep = ("wanbond_adaptive_", "wanbond_path_up", "wanbond_path_rtt", "wanbond_resequencer_skipped", "wanbond_resequencer_hol_holds")
def counters(device):
    base = "/sys/class/net/" + device + "/statistics/"
    return [int(open(base + name).read()) for name in ("rx_bytes", "tx_bytes")]
end = time.time() + float(sys.argv[1])
with open("/root/samples.jsonl", "w") as out:
    while time.time() < end:
        at = time.time()
        try:
            text = urllib.request.urlopen("http://127.0.0.1:9090/metrics", timeout=1).read().decode()
        except Exception:
            text = ""
        series = {}
        for line in text.splitlines():
            if line.startswith(keep):
                name, value = line.rsplit(" ", 1)
                series[name] = float(value)
        out.write(json.dumps({"t": at, "if": {d: counters(d) for d in ("wanbond0", "eth1", "eth2")}, "m": series}) + "\n")
        time.sleep(max(0, 0.1 - (time.time() - at)))
'''

STOP_VOICE = "if test -f /root/voice2.pid; then kill $(cat /root/voice2.pid) 2>/dev/null || true; rm /root/voice2.pid; fi"

# WAN 1 is the satellite link, WAN 2 the mobile link. "standby" is the production
# satellite plan of 2026-10: 0.5 Mbit/s each way, policed. "roam" is the same link on a
# full plan. Delays are one-way milliseconds. hub is the downlink, edge the uplink.
STANDBY = {"rate": 0.5, "delay": 14, "jitter": 0, "loss": 0, "police": True}
ROAM_DOWN = {"rate": 100, "delay": 14, "jitter": 0, "loss": 0, "buffer_ms": 100}
ROAM_UP = {"rate": 15, "delay": 14, "jitter": 0, "loss": 0, "buffer_ms": 100}
MOBILE_DOWN = {"rate": 50, "delay": 28, "jitter": 0, "loss": 0, "buffer_ms": 300}
MOBILE_UP = {"rate": 10, "delay": 28, "jitter": 0, "loss": 0, "buffer_ms": 300}

FIELD = {"hub": {1: STANDBY, 2: MOBILE_DOWN}, "edge": {1: STANDBY, 2: MOBILE_UP}}
UPGRADED = {"hub": {1: ROAM_DOWN, 2: MOBILE_DOWN}, "edge": {1: ROAM_UP, 2: MOBILE_UP}}


def both(lane, **change):
    """The same change to a WAN in both directions."""
    return [(guest, lane, change) for guest in GUESTS]


def to(lane, down, up):
    return [("hub", lane, down), ("edge", lane, up)]


SCENARIOS = {
    # One WAN goes silent, as in a tunnel, and returns; then the other.
    "blackout": {"profile": FIELD, "seconds": 80, "tcp": True, "events": [
        (20, "mobile dark", both(2, loss=100)), (35, "mobile back", both(2, loss=0)),
        (50, "satellite dark", both(1, loss=100)), (65, "satellite back", both(1, loss=0))]},
    "blackout-voice": {"profile": FIELD, "seconds": 80, "tcp": False, "events": [
        (20, "mobile dark", both(2, loss=100)), (35, "mobile back", both(2, loss=0)),
        (50, "satellite dark", both(1, loss=100)), (65, "satellite back", both(1, loss=0))]},
    "blackout-upgraded": {"profile": UPGRADED, "seconds": 80, "tcp": True, "events": [
        (20, "mobile dark", both(2, loss=100)), (35, "mobile back", both(2, loss=0)),
        (50, "satellite dark", both(1, loss=100)), (65, "satellite back", both(1, loss=0))]},
    # The mobile link loses four fifths of its rate and regains it; the satellite link is
    # put on the standby plan and back on the full one.
    "rate": {"profile": UPGRADED, "seconds": 120, "tcp": True, "events": [
        (20, "mobile 10/2", to(2, {"rate": 10}, {"rate": 2})), (40, "mobile 50/10", to(2, {"rate": 50}, {"rate": 10})),
        (60, "satellite standby", to(1, STANDBY, STANDBY)), (90, "satellite roam", to(1, ROAM_DOWN, ROAM_UP))]},
    # The production pair today, then the satellite plan is upgraded.
    "upgrade": {"profile": FIELD, "seconds": 90, "tcp": True, "events": [
        (30, "satellite roam", to(1, ROAM_DOWN, ROAM_UP))]},
    # The lane a call rides becomes the slow one and recovers; then the other lane
    # becomes the fastest.
    "latency": {"profile": UPGRADED, "seconds": 100, "tcp": True, "events": [
        (20, "satellite 120 ms", both(1, delay=120)), (40, "satellite 14 ms", both(1, delay=14)),
        (60, "mobile 5 ms", both(2, delay=5)), (80, "mobile 28 ms", both(2, delay=28))]},
    "latency-voice": {"profile": FIELD, "seconds": 100, "tcp": False, "events": [
        (20, "satellite 120 ms", both(1, delay=120)), (40, "satellite 14 ms", both(1, delay=14)),
        (60, "mobile 5 ms", both(2, delay=5)), (80, "mobile 28 ms", both(2, delay=28))]},
    "latency-field": {"profile": FIELD, "seconds": 100, "tcp": True, "events": [
        (20, "satellite 120 ms", both(1, delay=120)), (40, "satellite 14 ms", both(1, delay=14)),
        (60, "mobile 5 ms", both(2, delay=5)), (80, "mobile 28 ms", both(2, delay=28))]},
}


def impair(lab, guest, lane, rate, delay, loss, jitter, police=False, buffer_ms=None):
    """lab.impair with a buffer depth in milliseconds; returns the guest's clock when the change was in force."""
    buffer = 100 if buffer_ms is None else math.ceil(rate * 1e6 / 8 * buffer_ms / 1000 / 1200)
    limit = buffer + math.ceil(rate * 1e6 / 8 * (delay + jitter) / 1000 / 1200)
    interface = f"eth{lane}"
    shaping = f"tc class replace dev {interface} parent 1: classid 1:10 htb rate {rate}mbit ceil {rate}mbit burst 4k"
    policing = f"tc filter del dev {interface} parent 1: prio 1 2>/dev/null || true"
    if police:
        shaping = f"tc class replace dev {interface} parent 1: classid 1:10 htb rate 1000mbit ceil 1000mbit"
        policing += f"\ntc filter add dev {interface} parent 1: prio 1 protocol all matchall action police rate {rate}mbit burst 3k conform-exceed drop/pipe flowid 1:10"
    out = lab.execute(guest, f"""set -eu
if ! tc qdisc show dev {interface} | grep -q 'qdisc htb 1:'; then
  tc qdisc add dev {interface} root handle 1: htb default 10
fi
{shaping}
{policing}
tc qdisc replace dev {interface} parent 1:10 handle 10: netem delay {delay}ms {jitter}ms loss {loss}% limit {limit} seed {100 + lane + GUESTS.index(guest) * 10}
python3 -c 'import time; print(time.time())'
""", capture_output=True).stdout
    return float(out.strip().splitlines()[-1])


def clock_offset(lab, guest):
    """Guest clock minus host clock, from the tightest of a few exchanges."""
    best = None
    for _ in range(5):
        before = time.time()
        guest_now = float(lab.execute(guest, "python3 -c 'import time; print(time.time())'", capture_output=True).stdout)
        after = time.time()
        if best is None or after - before < best[0]:
            best = (after - before, guest_now - (before + after) / 2)
    return best[1], best[0] / 2


def run(args):
    scenario = SCENARIOS[args.scenario]
    lab = Lab()
    lab.acquire()
    output = lab.state / (time.strftime("%Y%m%d-%H%M%S") + "-adapt-" + args.scenario + (f"-{args.label}" if args.label else ""))
    output.mkdir()
    conditions = {guest: {lane: dict(scenario["profile"][guest][lane]) for lane in (1, 2)} for guest in GUESTS}
    with concurrent.futures.ThreadPoolExecutor() as pool:
        for future in [pool.submit(impair, lab, guest, lane, **conditions[guest][lane]) for guest in GUESTS for lane in (1, 2)]:
            future.result()
        provision(lab, args.binary)
        for guest in GUESTS:
            address = "10.77.0.1" if guest == "hub" else "10.77.0.2"
            lab.execute(guest, "cat > /root/voice2.py", input=VOICE)
            lab.execute(guest, "cat > /root/sampler.py", input=SAMPLER)
            lab.execute(guest, f"{STOP_VOICE}; nohup python3 /root/voice2.py server {address} > /root/voice-server.log 2>&1 < /dev/null & echo $! > /root/voice2.pid")
        offsets = {guest: clock_offset(lab, guest) for guest in GUESTS}
        seconds = scenario["seconds"]
        manifest = {"scenario": args.scenario, "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
                    "profile": conditions, "seconds": seconds, "tcp": scenario["tcp"], "clock_offsets": offsets, "events": []}
        if args.idle:
            time.sleep(args.idle)
        lab.execute("hub", "killall -q iperf3 || true; iperf3 -s -1 -D -B 10.77.0.1")
        for guest in GUESTS:
            lab.execute(guest, f"nohup python3 /root/sampler.py {seconds + 6} > /root/sampler.log 2>&1 < /dev/null &")
        time.sleep(1)
        manifest["start_host"] = time.time()
        start = time.monotonic()
        tcp = pool.submit(lab.execute, "edge", f"iperf3 -c 10.77.0.1 --bidir -l 1K -t {seconds} -J || true", capture_output=True) if scenario["tcp"] else None
        voices = {guest: pool.submit(lab.execute, guest, f"python3 /root/voice2.py client 10.77.0.{2 if guest == 'hub' else 1} {seconds}", capture_output=True) for guest in GUESTS}
        for offset, name, changes in scenario["events"]:
            time.sleep(max(0, start + offset - time.monotonic()))
            applied = []
            for guest, lane, change in changes:
                conditions[guest][lane] = {**{k: v for k, v in conditions[guest][lane].items() if "rate" not in change or k in ("delay", "jitter", "loss")}, **change}
                applied.append((guest, pool.submit(impair, lab, guest, lane, **conditions[guest][lane])))
            manifest["events"].append({"name": name, "planned": offset, "at_host": time.time(), "at_guest": {guest: future.result() for guest, future in applied},
                                       "conditions": json.loads(json.dumps(conditions))})
            print(f"{time.monotonic() - start:6.1f} s  {name}", flush=True)
        for guest, future in voices.items():
            (output / f"{guest}-voice.json").write_text(future.result().stdout)
        if tcp is not None:
            (output / "tcp.json").write_text(tcp.result().stdout)
        time.sleep(6)
        for guest in GUESTS:
            (output / f"{guest}-samples.jsonl").write_text(lab.execute(guest, "cat /root/samples.jsonl", capture_output=True).stdout)
            (output / f"{guest}-daemon.log").write_text(lab.execute(guest, "cat /root/wanbond.log", capture_output=True).stdout)
            lab.execute(guest, STOP_VOICE)
        (output / "scenario.json").write_text(json.dumps(manifest, indent=2))
    print(output, flush=True)
    show(output)


def quantile(values, q):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(len(ordered) * q))] if ordered else float("nan")


def lane_series(sample, name, lane):
    for key, value in sample["m"].items():
        if key.startswith(name + "{") and f'lane="{lane}"' in key:
            return value
    return float("nan")


def show(directory):
    directory = Path(directory)
    manifest = json.loads((directory / "scenario.json").read_text())
    seconds = manifest["seconds"]
    origin = manifest["start_host"]  # host clock
    offset = {guest: manifest["clock_offsets"][guest][0] for guest in GUESTS}
    samples = {guest: [json.loads(line) for line in (directory / f"{guest}-samples.jsonl").read_text().splitlines() if line] for guest in GUESTS}
    for guest in GUESTS:
        for sample in samples[guest]:
            sample["at"] = sample["t"] - offset[guest] - origin
    voice = {}
    for guest in GUESTS:
        report = json.loads((directory / f"{guest}-voice.json").read_text())
        begin = report["epoch"] - offset[guest] - origin
        got = {seq: (begin + at, rtt) for seq, at, rtt in report["samples"]}
        voice[guest] = {"begin": begin, "sent": report["sent"], "got": got}
    events = [(event["at_host"] - origin, event["name"]) for event in manifest["events"]]

    def at(guest, when):
        """The last sample of the guest not after the time."""
        chosen = None
        for sample in samples[guest]:
            if sample["at"] > when:
                break
            chosen = sample
        return chosen

    def megabits(guest, device, index, begin, end):
        first, last = at(guest, begin), at(guest, end)
        if first is None or last is None or last["at"] <= first["at"]:
            return float("nan")
        return (last["if"][device][index] - first["if"][device][index]) * 8 / 1e6 / (last["at"] - first["at"])

    hub_lanes, edge_lanes = ("0", "1"), ("0", "256")
    print(f"\n{directory.name}: per second. Mbit/s received on the tunnel and on each WAN; voice by time of sending\n"
          "(received/sent, median and highest round trip in ms); lane targets and estimates in Mbit/s (d = discovering,\n"
          "X = not up), downlink lanes from the hub, uplink lanes from the edge.")
    print(f"{'s':>3} | {'down':>6} {'sat':>5} {'mob':>6} | {'up':>5} {'sat':>5} {'mob':>5} | {'voice edge':>18} | {'voice hub':>18} | "
          f"{'down sat tgt/est':>16} {'down mob tgt/est':>16} | {'up sat tgt/est':>15} {'up mob tgt/est':>15}")
    for second in range(seconds):
        marks = [name for when, name in events if second <= when < second + 1]
        row = [f"{second:3d}",
               f"{megabits('edge', 'wanbond0', 0, second, second + 1):6.2f} {megabits('edge', 'eth1', 0, second, second + 1):5.2f} {megabits('edge', 'eth2', 0, second, second + 1):6.2f}",
               f"{megabits('hub', 'wanbond0', 0, second, second + 1):5.2f} {megabits('hub', 'eth1', 0, second, second + 1):5.2f} {megabits('hub', 'eth2', 0, second, second + 1):5.2f}"]
        for guest in ("edge", "hub"):
            v = voice[guest]
            sent = [seq for seq in range(v["sent"]) if second <= v["begin"] + seq * 0.02 < second + 1]
            rtts = [v["got"][seq][1] for seq in sent if seq in v["got"]]
            row.append(f"{len(rtts):2d}/{len(sent):2d} {statistics.median(rtts) if rtts else float('nan'):5.0f} {max(rtts) if rtts else float('nan'):5.0f}")
        for guest, lanes in (("hub", hub_lanes), ("edge", edge_lanes)):
            sample = at(guest, second + 1)
            cells = []
            for lane in lanes:
                if sample is None:
                    cells.append("-")
                    continue
                target = lane_series(sample, "wanbond_adaptive_target_rate_bytes_per_second", lane) * 8 / 1e6
                estimate = lane_series(sample, "wanbond_adaptive_capacity_bytes_per_second", lane) * 8 / 1e6
                flag = "X" if lane_series(sample, "wanbond_adaptive_up", lane) == 0 else "d" if lane_series(sample, "wanbond_adaptive_discovering", lane) == 1 else " "
                cells.append(f"{target:7.2f}/{estimate:6.2f}{flag}")
            row.append(" ".join(cells))
        print(" | ".join(row) + ("   <== " + ", ".join(marks) if marks else ""))

    print("\nPhases (from each change to the next): tunnel Mbit/s in the first 5 s and afterwards; voice lost, longest gap\n"
          "between arrivals, round trip p50/p99 in the first 5 s and afterwards.")
    bounds = [(0.0, "start")] + events + [(float(seconds), "end")]
    summary = []
    for (begin, name), (end, _) in zip(bounds, bounds[1:]):
        early = min(end, begin + 5)
        line = {"phase": name, "begin": round(begin, 2), "end": round(end, 2),
                "down_first5": megabits("edge", "wanbond0", 0, begin, early), "down_rest": megabits("edge", "wanbond0", 0, early, end),
                "up_first5": megabits("hub", "wanbond0", 0, begin, early), "up_rest": megabits("hub", "wanbond0", 0, early, end)}
        text = f"  {name:18s} {begin:5.1f}-{end:5.1f} s  down {line['down_first5']:6.2f} then {line['down_rest']:6.2f}  up {line['up_first5']:5.2f} then {line['up_rest']:5.2f}"
        for guest in ("edge", "hub"):
            v = voice[guest]
            sent = [seq for seq in range(v["sent"]) if begin <= v["begin"] + seq * 0.02 < end]
            arrived = sorted(v["got"][seq][0] for seq in sent if seq in v["got"])
            gap = max([b - a for a, b in zip(arrived, arrived[1:])] or [float("nan")])
            first = [v["got"][seq][1] for seq in sent if seq in v["got"] and v["begin"] + seq * 0.02 < early]
            rest = [v["got"][seq][1] for seq in sent if seq in v["got"] and v["begin"] + seq * 0.02 >= early]
            line[guest] = {"sent": len(sent), "lost": len(sent) - len(arrived), "max_gap_ms": gap * 1000,
                           "first5_p50": quantile(first, 0.5), "first5_p99": quantile(first, 0.99), "rest_p50": quantile(rest, 0.5), "rest_p99": quantile(rest, 0.99)}
            text += (f" | voice {guest}: lost {len(sent) - len(arrived):3d}/{len(sent):4d} gap {gap * 1000:4.0f} ms rtt {quantile(first, 0.5):4.0f}/{quantile(first, 0.99):4.0f}"
                     f" then {quantile(rest, 0.5):4.0f}/{quantile(rest, 0.99):4.0f}")
        summary.append(line)
        print(text)
    (directory / "summary.json").write_text(json.dumps(summary, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="action", required=True)
    runner = sub.add_parser("run")
    runner.add_argument("binary", type=Path)
    runner.add_argument("scenario", choices=sorted(SCENARIOS))
    runner.add_argument("--label", default="")
    runner.add_argument("--idle", type=float, default=0, help="seconds to leave the tunnel idle before the traffic starts")
    shower = sub.add_parser("show")
    shower.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.action == "run":
        run(args)
    else:
        show(args.directory)


if __name__ == "__main__":
    main()
