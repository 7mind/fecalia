#!/usr/bin/env python3
"""Two KVM guests with independent management and WAN networks. No host networking changes."""

import argparse
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import shlex
import socket
import subprocess
import time
import urllib.request


IMAGE = "alpine-3.24.2-x86_64-cloudinit-r0.qcow2"
IMAGE_URL = f"https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/cloud/{IMAGE}"
IMAGE_SHA512 = "c9504d23613f304e0cfb6f5fec872e29e5a5a62e2bc64796daf19c14fdddaa87dc252912fd8bdd17f7b8ebf0cd03305e4075993d54de175e6028d6b50414c67f"
GUESTS = ("hub", "edge")
# Redraws an interface's shaped rate every grant, in the guest.
VARY_RATE = """import random, subprocess, sys, time
interface, rate, swing, grant, seed = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), float(sys.argv[4]) / 1000, int(sys.argv[5])
draw = random.Random(seed)
deadline = time.monotonic()
while True:
    current = f"{rate * (1 + swing * (2 * draw.random() - 1)):.3f}mbit"
    subprocess.run(["tc", "class", "change", "dev", interface, "parent", "1:", "classid", "1:10", "htb", "rate", current, "ceil", current, "burst", "4k"], check=True)
    deadline += grant
    time.sleep(max(0, deadline - time.monotonic()))
"""


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


class Lab:
    def __init__(self):
        self.root = Path(os.environ["YOLO_VM_STATE_DIR"]).resolve()
        self.state = (self.root / "wanbond-bonding").resolve()
        if not self.state.is_relative_to(self.root):
            raise ValueError("lab directory escapes VM state root")
        self.state.mkdir(mode=0o700, exist_ok=True)
        self.state.chmod(0o700)
        self.manifest = self.state / "lab.json"
        self.key = self.state / "id_ed25519"

    def disk(self, path):
        resolved = path.resolve(strict=True)
        if not resolved.is_relative_to(self.root) or not resolved.is_file():
            raise ValueError(f"guest disk is not a regular file under {self.root}: {resolved}")
        return str(resolved)

    def acquire(self):
        self.lock_file = (self.state / "owner.lock").open("w")
        try:
            fcntl.flock(self.lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("another VM scenario owns this lab") from None

    def ssh_args(self, guest):
        meta = json.loads(self.manifest.read_text())[guest]
        return ["ssh", "-F", "/dev/null", "-i", str(self.key), "-p", str(meta["port"]),
                "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=5",
                "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", f"ControlPath={self.state}/mux-%C",
                "-o", "StrictHostKeyChecking=accept-new", "-o", f"UserKnownHostsFile={self.state}/known_hosts",
                "root@127.0.0.1"]

    def execute(self, guest, command, **kwargs):
        return run(self.ssh_args(guest) + [command], **kwargs)

    def put(self, guest, source, target):
        with open(source, "rb") as data:
            self.execute(guest, f"umask 077; cat > {shlex.quote(target)}", stdin=data)

    def up(self):
        self.acquire()
        if self.manifest.exists():
            raise RuntimeError(f"lab exists: {self.manifest}; use status or stop first")
        if os.environ.get("SMIND_SANDBOXED") != "1" or not os.access("/dev/kvm", os.R_OK | os.W_OK):
            raise RuntimeError("requires yolo and accessible /dev/kvm")
        base = self.root / "images" / "alpine" / IMAGE
        base.parent.mkdir(parents=True, exist_ok=True)
        if not base.exists():
            temporary = base.with_suffix(".download")
            urllib.request.urlretrieve(IMAGE_URL, temporary)
            temporary.rename(base)
        with open(base, "rb") as image:
            if hashlib.file_digest(image, "sha512").hexdigest() != IMAGE_SHA512:
                raise RuntimeError("Alpine image SHA512 mismatch")
        base.chmod(0o444)
        if not self.key.exists():
            run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(self.key)])
        password = run(["mkpasswd", "-m", "sha-512", "-s"], input=secrets.token_urlsafe(48), capture_output=True).stdout.strip()
        public = self.key.with_suffix(".pub").read_text().strip()
        meta = {}
        reused = {}
        for index, guest in enumerate(GUESTS):
            directory = self.state / guest
            directory.mkdir(mode=0o700, exist_ok=True)
            disk = directory / "disk.qcow2"
            existing = disk.exists()
            reused[guest] = existing
            if not existing:
                run(["qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", self.disk(base), str(disk), "8G"])
            seed = directory / "seed.img"
            if not existing:
                user_data = directory / "user-data"
                user_data.write_text(f'''#cloud-config
hostname: wanbond-{guest}
disable_root: false
ssh_pwauth: false
users:
  - name: root
    lock_passwd: false
    passwd: {password}
    ssh_authorized_keys:
      - {public}
packages: [iproute2, iproute2-tc, iperf3, python3, tcpdump, wireguard-tools, curl]
write_files:
  - path: /etc/ssh/sshd_config.d/00-wanbond-lab.conf
    content: |
      PasswordAuthentication no
      KbdInteractiveAuthentication no
      PermitRootLogin prohibit-password
runcmd:
  - modprobe tun
  - modprobe sch_netem
  - modprobe sch_htb
  - rc-service sshd restart
  - apk info -v > /root/lab-packages.txt
  - touch /root/lab-ready
  - echo WANBOND_LAB_READY > /dev/ttyS0
''')
                metadata = directory / "meta-data"
                metadata.write_text(f"instance-id: wanbond-{guest}-{secrets.token_hex(8)}\nlocal-hostname: wanbond-{guest}\n")
                run(["cloud-localds", str(seed), str(user_data), str(metadata)])
            (directory / "serial.log").write_text("")
            with socket.socket() as listener:
                listener.bind(("127.0.0.1", 0))
                port = listener.getsockname()[1]
            args = ["qemu-system-x86_64", "-name", f"wanbond-lab-{guest}", "-machine", "q35,accel=kvm",
                    "-cpu", "host", "-smp", "4", "-m", "2048", "-display", "none",
                    "-serial", f"file:{directory}/serial.log", "-qmp", f"unix:{directory}/qmp.sock,server=on,wait=off",
                    "-drive", f"file={self.disk(disk)},if=virtio,format=qcow2",
                    "-drive", f"file={self.disk(seed)},if=virtio,format=raw,readonly=on",
                    "-netdev", f"user,id=mgmt,hostfwd=tcp:127.0.0.1:{port}-:22",
                    "-device", f"virtio-net-pci,netdev=mgmt,mac=52:54:00:ab:{index:02x}:01"]
            for lane in (1, 2):
                server = "on" if guest == "hub" else "off"
                backend = f"stream,id=wan{lane},server={server},addr.type=unix,addr.path={self.state}/wan{lane}.sock"
                if guest == "edge":
                    backend += ",reconnect-ms=1000"
                args += ["-netdev", backend, "-device", f"virtio-net-pci,netdev=wan{lane},mac=52:54:00:ab:{index:02x}:{lane + 1:02x}"]
            with open(directory / "qemu.log", "w") as log:
                process = subprocess.Popen(args, stdout=log, stderr=log, start_new_session=True)
            meta[guest] = {"pid": process.pid, "port": port}
            self.manifest.write_text(json.dumps(meta, indent=2))
            time.sleep(1)
            if process.poll() is not None:
                raise RuntimeError((directory / "qemu.log").read_text())
        deadline = time.monotonic() + 240
        for guest in GUESTS:
            serial = self.state / guest / "serial.log"
            if reused[guest]:
                retry_delay = 2
                while True:
                    probe = subprocess.run(self.ssh_args(guest) + ["true"], capture_output=True, text=True)
                    if probe.returncode == 0:
                        break
                    (self.state / guest / "ssh-readiness.log").write_text(probe.stderr)
                    if "Permission denied" in probe.stderr or time.monotonic() >= deadline:
                        raise RuntimeError(f"{guest} SSH readiness failed: {probe.stderr}")
                    time.sleep(retry_delay)
                    retry_delay = min(15, 2*retry_delay)
            else:
                while "WANBOND_LAB_READY" not in serial.read_text(errors="replace"):
                    if time.monotonic() >= deadline:
                        raise TimeoutError(f"boot timeout: inspect {serial}")
                    time.sleep(2)
            self.execute(guest, "set -eu; test -f /root/lab-ready; ip -br link; for cmd in tc iperf3 python3 wg; do command -v $cmd; done")
            policy = self.execute(guest, "sshd -T", capture_output=True).stdout
            for required in ("passwordauthentication no", "kbdinteractiveauthentication no", "permitrootlogin prohibit-password"):
                if required not in policy:
                    raise RuntimeError(f"{guest}: SSH policy missing {required}")
            probe = subprocess.run(self.ssh_args(guest)[:-1] + ["-S", "none", "-vv", "-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=password,keyboard-interactive", "root@127.0.0.1", "true"], capture_output=True, text=True)
            (self.state / guest / "ssh-disallowed-methods.log").write_text(probe.stderr)
            if probe.returncode != 255 or "Authentications that can continue: publickey" not in probe.stderr:
                raise RuntimeError(f"{guest}: SSH rejection probe inconclusive; inspect its log")
        self.network()
        print(f"Lab ready: {self.state}", flush=True)

    def network(self):
        for guest in GUESTS:
            suffix = 1 if guest == "hub" else 2
            commands = ["set -eu", "modprobe tun", "modprobe sch_netem", "modprobe sch_htb", "test -c /dev/net/tun", "sysctl -w net.ipv4.conf.all.rp_filter=0", "sysctl -w net.ipv4.conf.default.rp_filter=0",
                        # A 300 Mbit/s path with 150 ms of round trip needs a 6 MB TCP
                        # window; the kernel's default ceiling of 6 MB would cap the
                        # test at the tunnel's round trip rather than its capacity.
                        "sysctl -w net.core.rmem_max=67108864", "sysctl -w net.core.wmem_max=67108864",
                        "sysctl -w net.ipv4.tcp_rmem='4096 131072 67108864'", "sysctl -w net.ipv4.tcp_wmem='4096 131072 67108864'"]
            for lane in (1, 2):
                commands += [f"ip link set eth{lane} up", f"ip addr replace 10.77.{lane}.{suffix}/24 dev eth{lane}",
                             f"sysctl -w net.ipv4.conf.eth{lane}.rp_filter=0"]
                if guest == "edge":
                    commands += [f"ip route replace 10.77.{lane}.0/24 dev eth{lane} table {100 + lane}",
                                 f"ip route replace 10.77.200.1/32 via 10.77.{lane}.1 dev eth{lane} table {100 + lane}",
                                 f"ip rule add priority {100 + lane} from 10.77.{lane}.2 table {100 + lane}"]
            commands += (["ip addr replace 10.77.200.1/32 dev lo"] if guest == "hub" else
                         ["ip route replace 10.77.200.1/32 via 10.77.1.1 dev eth1"])
            self.execute(guest, "\n".join(commands))
        self.execute("edge", "ping -c 2 -I 10.77.1.2 10.77.200.1 && ping -c 2 -I 10.77.2.2 10.77.200.1")

    def impair(self, guest, lane, rate, delay, loss, jitter, correlation=0, police=False, buffer_ms=None, swing=0, grant_ms=0):
        """correlation is netem's delay correlation in percent: successive
        delays stay close, as on a radio link whose latency drifts rather than
        scatters, so a fast link does not reorder thousands of datagrams.

        police drops what exceeds the rate instead of queueing it, with a
        burst of two datagrams: overload then shows as loss and not as delay,
        as it does on the production satellite link.

        buffer_ms sizes the router buffer in milliseconds of traffic at the
        rate instead of 100 packets. swing and grant_ms make the rate vary, as
        a cellular scheduler's grant does: every grant_ms it is redrawn
        uniformly within rate*(1±swing), so a queue forms whenever a grant
        falls below the sending rate."""
        if guest not in GUESTS or lane not in (1, 2) or rate <= 0 or delay < jitter or jitter < 0 or not 0 <= loss <= 100 or not 0 <= correlation < 100:
            raise ValueError("invalid guest/lane or impairment values")
        if not 0 <= swing < 1 or (swing > 0) != (grant_ms > 0) or (buffer_ms is not None and buffer_ms <= 0) or (police and swing > 0):
            raise ValueError("invalid buffer or rate variation")
        # netem holds propagation traffic as well as the router queue. Reserve
        # its bandwidth-delay product before adding the router buffer.
        buffer = 100 if buffer_ms is None else math.ceil(rate * 1e6 / 8 * buffer_ms / 1000 / 1200)
        limit = buffer + math.ceil(rate * 1e6 / 8 * (delay + jitter) / 1000 / 1200)
        interface = f"eth{lane}"
        seed = 100 + lane + GUESTS.index(guest) * 10
        shaping = f"tc class replace dev {interface} parent 1: classid 1:10 htb rate {rate}mbit ceil {rate}mbit burst 4k"
        policing = f"tc filter del dev {interface} parent 1: prio 1 2>/dev/null || true"
        if police:
            shaping = f"tc class replace dev {interface} parent 1: classid 1:10 htb rate 1000mbit ceil 1000mbit"
            policing += f"\ntc filter add dev {interface} parent 1: prio 1 protocol all matchall action police rate {rate}mbit burst 3k conform-exceed drop/pipe flowid 1:10"
        self.execute(guest, f"""set -eu
if test -f /root/vary-{interface}.pid; then kill $(cat /root/vary-{interface}.pid) 2>/dev/null || true; rm /root/vary-{interface}.pid; fi
if ! tc qdisc show dev {interface} | grep -q 'qdisc htb 1:'; then
  tc qdisc add dev {interface} root handle 1: htb default 10
fi
{shaping}
{policing}
tc qdisc replace dev {interface} parent 1:10 handle 10: netem delay {delay}ms {jitter}ms {correlation}% loss {loss}% limit {limit} seed {seed}
tc -s qdisc show dev {interface}
""")
        if swing:
            self.execute(guest, f"""set -eu
cat > /root/vary.py <<'VARY'
{VARY_RATE}VARY
nohup python3 /root/vary.py {interface} {rate} {swing} {grant_ms} {seed} > /root/vary-{interface}.log 2>&1 < /dev/null &
echo $! > /root/vary-{interface}.pid
""")

    def stop(self):
        self.acquire()
        meta = json.loads(self.manifest.read_text())
        for guest in meta:
            self.execute(guest, "poweroff")
        deadline = time.monotonic() + 45
        for guest, entry in meta.items():
            while Path(f"/proc/{entry['pid']}/cmdline").exists() and Path(f"/proc/{entry['pid']}/cmdline").read_bytes():
                if time.monotonic() >= deadline:
                    raise TimeoutError(f"{guest} did not shut down; disk retained, no forced termination")
                time.sleep(1)
        self.manifest.rename(self.state / "stopped.json")
        print("Stopped; guest disks and results retained.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    for name in ("up", "stop", "status"):
        sub.add_parser(name)
    execute = sub.add_parser("exec")
    execute.add_argument("guest", choices=GUESTS)
    execute.add_argument("command")
    put = sub.add_parser("put")
    put.add_argument("guest", choices=GUESTS)
    put.add_argument("source")
    put.add_argument("target")
    impair = sub.add_parser("impair")
    impair.add_argument("guest", choices=GUESTS)
    impair.add_argument("lane", type=int, choices=(1, 2))
    impair.add_argument("--rate", type=float, required=True, help="Mbit/s")
    impair.add_argument("--delay", type=float, required=True, help="one-way milliseconds")
    impair.add_argument("--loss", type=float, required=True, help="percent")
    impair.add_argument("--jitter", type=float, default=0, help="uniform delay variation in milliseconds")
    args = parser.parse_args()
    lab = Lab()
    if args.action == "exec":
        lab.execute(args.guest, args.command)
    elif args.action == "put":
        lab.put(args.guest, args.source, args.target)
    elif args.action == "impair":
        if args.rate <= 0 or args.delay < 0 or not 0 <= args.loss <= 100:
            parser.error("rate > 0, delay >= 0 and 0 <= loss <= 100 required")
        lab.acquire()
        lab.impair(args.guest, args.lane, args.rate, args.delay, args.loss, args.jitter)
    elif args.action == "status":
        print(lab.manifest.read_text())
        for guest in GUESTS:
            lab.execute(guest, "uptime; ip -br addr; tc -s qdisc show")
    else:
        getattr(lab, args.action)()


if __name__ == "__main__":
    main()
