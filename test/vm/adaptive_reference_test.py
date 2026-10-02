"""Behavioral-Blackbox-Atomic budget invariants, independent of the controller."""

import json
from pathlib import Path
import unittest

from adaptive_reference import LAB_BUDGET, calibrated_services, payload_reference


class ReferenceTests(unittest.TestCase):
    def test_each_direction_fits_voice_data_reverse_ack_and_feedback(self):
        services = ((62500, 50000), (12500000, 156250))
        payload = payload_reference(services, True, LAB_BUDGET)
        fixed = 2 * (207 / 0.025 + 143 / 0.2) + 100 * 367
        wire = [sum(lane[side] for lane in services) for side in range(2)]
        for side in range(2):
            used = fixed + payload[side] / 1287 * 1514 + payload[1-side] / 1287 * (239 + 207 / 64)
            self.assertLessEqual(used, wire[side] + 1e-8)
        self.assertTrue(any(abs(fixed + payload[side] / 1287 * 1514 + payload[1-side] / 1287 * (239 + 207 / 64) - wire[side]) < 1e-8 for side in range(2)))

    def test_one_way_outage_uses_the_same_survivor_as_two_way(self):
        survivor = ((62500, 50000),)
        expected = payload_reference(survivor, True, LAB_BUDGET)
        self.assertEqual(payload_reference(survivor + ((12500000, 0),), True, LAB_BUDGET), expected)
        self.assertEqual(payload_reference(survivor + ((0, 156250),), True, LAB_BUDGET), expected)

    def test_voice_reduces_both_available_payload_rates(self):
        services = ((37500000, 37500000), (37500000, 37500000))
        without = payload_reference(services, False, LAB_BUDGET)
        with_voice = payload_reference(services, True, LAB_BUDGET)
        self.assertTrue(all(a > b > 0 for a, b in zip(without, with_voice)))

    def test_no_reference_when_required_traffic_exceeds_service(self):
        with self.assertRaisesRegex(ValueError, "no bidirectional TCP service"):
            payload_reference(((1000, 1000),), True, LAB_BUDGET)

    def test_calibration_uses_udp_even_when_tcp_is_congestion_limited(self):
        profile = json.loads(Path(__file__).with_name("profiles").joinpath("gigaradio.json").read_text())
        measurements = {f"{protocol}-{direction}-wan{lane}": 290 if protocol == "udp" else 7
                        for protocol in ("udp", "tcp") for direction in ("uplink", "downlink") for lane in (1, 2)}
        summary = {"profile": profile, "measurements": measurements}
        services = calibrated_services(summary)
        self.assertTrue(all(0.85 * 37500000 <= rate <= 37500000 for lane in services for rate in lane))
        measurements["udp-downlink-wan1"] = 254
        with self.assertRaisesRegex(ValueError, "failed UDP capacity calibration"):
            calibrated_services(summary)


if __name__ == "__main__":
    unittest.main()
