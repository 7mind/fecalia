"""Independent TCP payload budget over calibrated bidirectional WAN service."""

from dataclasses import dataclass
import math


@dataclass(frozen=True)
class ProtocolBudget:
    tcp_payload: int
    tcp_data_wire: int
    tcp_ack_wire: int
    voice_wire: int
    feedback_wire: int
    feedback_interval: float
    keepalive_wire: int
    keepalive_interval: float


# Observed lab TUN MTU 1339, TCP MSS 1287 and enabled TCP timestamps.
# WireGuard caps full-packet padding at the TUN MTU; link bytes include Ethernet.
LAB_BUDGET = ProtocolBudget(1287, 1514, 239, 367, 207, 0.025, 143, 0.2)
VOICE_PACKETS_PER_SECOND = 100
RECEIPTS_PER_FEEDBACK = 64
CALIBRATION_UDP_PAYLOAD = 1300
IP_UDP_ETHERNET_BYTES = 42
MINIMUM_CALIBRATION_SHARE = 0.85


def payload_reference(services: tuple[tuple[float, float], ...], voice: bool, budget: ProtocolBudget) -> tuple[float, float]:
    """Bytes/s in each direction at equal utilization of available wire budgets.

    services contains (downlink, uplink) wire bytes/s for each WAN. Outages,
    including one-way outages, use the survivor reference required by 1a/1c.
    Candidate estimates, copies, repairs and losses are not inputs.
    """
    if not services or any(len(lane) != 2 or any(not math.isfinite(rate) or rate < 0 for rate in lane) for lane in services):
        raise ValueError("WAN services must be finite nonnegative directional pairs")
    live = [lane for lane in services if min(lane) > 0]
    fixed = budget.feedback_wire / budget.feedback_interval + budget.keepalive_wire / budget.keepalive_interval
    voice_bytes = VOICE_PACKETS_PER_SECOND * budget.voice_wire if voice else 0
    available = [sum(lane[side] for lane in live) - len(live) * fixed - voice_bytes for side in range(2)]
    if min(available) <= 0:
        raise ValueError("required traffic leaves no bidirectional TCP service")
    desired = [rate / budget.tcp_data_wire for rate in available]
    ack_wire = budget.tcp_ack_wire + budget.feedback_wire / RECEIPTS_PER_FEEDBACK
    gain = min(available[side] / (desired[side] * budget.tcp_data_wire + desired[1-side] * ack_wire) for side in range(2))
    return tuple(rate * gain * budget.tcp_payload for rate in desired)


def calibrated_services(summary):
    """Convert successful fixed-size UDP receipts to per-WAN wire service."""
    services = []
    for lane in ("1", "2"):
        directional = []
        for direction, sender in (("downlink", "hub"), ("uplink", "edge")):
            nominal = summary["profile"][sender][lane]["rate"]
            received = summary["measurements"][f"udp-{direction}-wan{lane}"]
            if not math.isfinite(received) or received < MINIMUM_CALIBRATION_SHARE * nominal:
                raise ValueError(f"WAN{lane} {direction} failed UDP capacity calibration")
            wire = received * (CALIBRATION_UDP_PAYLOAD + IP_UDP_ETHERNET_BYTES) / CALIBRATION_UDP_PAYLOAD
            directional.append(min(nominal, wire) * 1e6 / 8)
        services.append(tuple(directional))
    return tuple(services)
