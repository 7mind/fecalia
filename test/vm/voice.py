#!/usr/bin/env python3
"""50 Hz, 160-byte UDP echo stream; records RTT, loss and receive gaps."""

import argparse
import json
import select
import socket
import struct
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("server", "client"))
    parser.add_argument("address")
    parser.add_argument("--seconds", type=int, default=60)
    args = parser.parse_args()
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        if args.mode == "server":
            sock.bind((args.address, 5202))
            while True:
                data, source = sock.recvfrom(2048)
                sock.sendto(data, source)
        sock.connect((args.address, 5202))
        sock.setblocking(False)
        start = time.monotonic()
        next_send = start
        sent = 0
        samples = []
        seen = set()
        end = start + args.seconds
        while time.monotonic() < end + 1:
            now = time.monotonic()
            if now >= next_send and now < end:
                packet = struct.pack("!Qd", sent, now) + bytes(144)
                sock.send(packet)
                sent += 1
                next_send += 0.020
            readable, _, _ = select.select([sock], [], [], max(0, min(0.020, next_send-now)) if now < end else 0.020)
            if readable:
                packet = sock.recv(2048)
                received = time.monotonic()
                seq, timestamp = struct.unpack("!Qd", packet[:16])
                if seq in seen:
                    raise RuntimeError("duplicate escaped tunnel")
                seen.add(seq)
                samples.append({"seq": seq, "at": received-start, "rtt_ms": (received-timestamp)*1000})
        delays = sorted(s["rtt_ms"] for s in samples)
        gaps = [b["at"]-a["at"] for a, b in zip(samples, samples[1:])]
        if samples:
            gaps += [samples[0]["at"], max(0, args.seconds-samples[-1]["at"])]
        report = {"sent": sent, "received": len(samples), "loss_percent": 100*(sent-len(samples))/sent,
                  "p99_rtt_ms": delays[min(len(delays)-1, int(len(delays)*0.99))] if delays else None,
                  "max_gap_ms": 1000*max(gaps) if gaps else None, "samples": samples}
        print(json.dumps(report))


if __name__ == "__main__":
    main()
