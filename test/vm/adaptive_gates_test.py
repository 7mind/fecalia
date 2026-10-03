"""Behavioral-Blackbox-Atomic checks of the policy acceptance evaluator."""

import unittest

from adaptive_gates import Interval, bulk_gate, latency, receiver_intervals, route_gate, voice_continuity, voice_rtts


class GateTests(unittest.TestCase):
    def test_one_percent_loss_and_150ms_gap_fail(self):
        report = {"sent": 100, "samples": [[seq, seq * .02, 20] for seq in range(100) if seq != 50]}
        checks = voice_continuity("voice", report, False)
        self.assertEqual(checks[0].status, "fail")
        report["sent"] = 2
        report["samples"] = [[0, 0, 20], [1, .15, 20]]
        self.assertEqual(voice_continuity("voice", report, False)[2].status, "fail")

    def test_four_consecutive_lost_fail_even_below_one_percent(self):
        report = {"sent": 1000, "samples": [[seq, seq * .02, 20] for seq in range(1000) if seq not in range(50, 54)]}
        checks = voice_continuity("voice", report, False)
        self.assertEqual(checks[0].status, "pass")
        self.assertEqual(checks[1].status, "fail")

    def test_voice_window_uses_actual_send_stamp(self):
        report = {"samples": [[0, 1.1, 200], [1, 1.2, 100], [2, 1.5, 150]]}
        self.assertEqual(voice_rtts(report, 10, 11, 11.4), [100, 150])

    def test_under_150_is_strict_and_empty_latency_is_inconclusive(self):
        report = {"samples": [[0, .15, 150], [1, .17, 150]]}
        self.assertEqual(latency("latency", report, 0, 0, 1, 150, .99, True).status, "fail")
        self.assertEqual(latency("latency", report, 0, 0, 1, 150, .99, False).status, "pass")
        self.assertEqual(latency("latency", report, 0, 2, 3, 150, .99, True).status, "inconclusive")

    def test_tcp_uses_receiver_bytes_and_millisecond_clock(self):
        report = {"start": {"timestamp": {"timemillisecs": 100250}}, "test_started_guest": 100.25, "intervals": [{"streams": [
            {"sender": True, "omitted": False, "start": 0, "end": 1, "bytes": 1000},
            {"sender": False, "omitted": False, "start": 0, "end": 1, "bytes": 100}]}]}
        intervals = receiver_intervals(report, .05, 100)
        self.assertEqual(len(intervals), 1)
        self.assertAlmostEqual(intervals[0].begin, .2)
        self.assertAlmostEqual(intervals[0].end, 1.2)
        self.assertEqual(intervals[0].bytes, 100)

    def test_connection_timestamp_is_not_measurement_origin(self):
        report = {"start": {"timestamp": {"timemillisecs": 100250}}, "test_started_guest": 101.25,
                  "intervals": [{"streams": [{"sender": False, "omitted": False, "start": 0, "end": 1, "bytes": 100}]}]}
        intervals = receiver_intervals(report, .05, 100)
        self.assertAlmostEqual(intervals[0].begin, 1.2)

    def test_missing_reference_cannot_pass_bulk(self):
        intervals = [Interval(second, second + 1, 100) for second in range(10)]
        self.assertEqual(bulk_gate("bulk", intervals, 0, 10, 3, None, .75, True, 0)[0].status, "inconclusive")
        self.assertTrue(all(check.status == "pass" for check in bulk_gate("bulk", intervals, 0, 10, 3, 100, .75, True, 0)))
        self.assertEqual(bulk_gate("bulk", intervals, 0, 10, 3, 200, .75, True, 0)[0].status, "fail")

    def test_partial_deadline_interval_cannot_create_a_false_failure(self):
        intervals = [Interval(.5, 1.5, 100), Interval(1.5, 2.5, 100)]
        checks = bulk_gate("bulk", intervals, 0, 3, 2, 150, .75, False, 0)
        self.assertEqual(checks[0].status, "inconclusive")

    def test_uncertain_receiver_origin_cannot_create_a_deadline_pass(self):
        intervals = [Interval(2, 3, 100)]
        checks = bulk_gate("bulk", intervals, 2, 5, 3, 100, .75, False, 1)
        self.assertEqual(checks[0].status, "inconclusive", "receiver interval might belong entirely before the change")

    def test_route_needs_originals_on_the_correct_guest_lane_ids(self):
        samples = [{"at": at, "m": {f'wanbond_adaptive_realtime_original_packets_total{{peer="hub",lane="{lane}"}}': count
                                     for lane, count in (("0", 0), ("256", at * 50))}} for at in (0, 1, 2)]
        self.assertEqual(route_gate("route", samples, 0, 3, ("0", "256"), "256").status, "pass")
        samples[1]["m"] = {}
        samples[2]["m"] = {}
        self.assertEqual(route_gate("route", samples, 0, 3, ("0", "256"), "256").status, "inconclusive")


if __name__ == "__main__":
    unittest.main()
