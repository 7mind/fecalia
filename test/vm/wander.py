#!/usr/bin/env python3
"""Moves a WAN's emulated delay over time, as a radio link's latency does.

netem draws its jitter anew for every datagram. Measured links differ: their
latency scatters with a memory of tens of milliseconds, and a satellite
link's also moves between levels that last for seconds. This changes the
netem delay of one interface every STEP seconds to follow such a walk; the
interface's rate limit and loss are left as the profile set them.
"""

import argparse
import math
import random
import subprocess
import time

STEP = 0.02


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("interface")
    parser.add_argument("--delay", type=float, required=True, help="mean one-way delay, ms")
    parser.add_argument("--scatter", type=float, required=True, help="standard deviation of the walk, ms")
    parser.add_argument("--memory", type=float, default=0.05, help="correlation time of the walk, seconds")
    parser.add_argument("--shift", type=float, default=0, help="level changes within +-SHIFT ms")
    parser.add_argument("--shift-every", type=float, default=15, help="seconds between level changes")
    parser.add_argument("--loss", type=float, default=0, help="random loss, percent")
    parser.add_argument("--limit", type=int, required=True, help="netem queue limit, datagrams")
    parser.add_argument("--seed", type=int, default=1)
    args = parser.parse_args()
    rng = random.Random(args.seed)
    walk, level, shifted = 0.0, 0.0, time.monotonic()
    next_step = time.monotonic()
    while True:
        now = time.monotonic()
        if args.shift and now - shifted >= args.shift_every:
            level, shifted = rng.uniform(-args.shift, args.shift), now
        a = STEP / args.memory
        walk += -walk * a + args.scatter * math.sqrt(2 * a) * rng.gauss(0, 1)
        walk = max(-2 * args.scatter, min(2 * args.scatter, walk))
        delay = max(0.5, args.delay + level + walk)
        subprocess.run(["tc", "qdisc", "change", "dev", args.interface, "parent", "1:10", "handle", "10:", "netem",
                        "delay", f"{delay:.2f}ms", "loss", f"{args.loss}%", "limit", str(args.limit)], check=True)
        next_step += STEP
        time.sleep(max(0, next_step - time.monotonic()))


if __name__ == "__main__":
    main()
