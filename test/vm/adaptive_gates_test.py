"""Behavioral-Blackbox-Atomic checks of the policy acceptance evaluator."""

import json
from pathlib import Path
import tempfile
import unittest

from adaptive_gates import Interval, TimeBounds, bounded_latency, bulk_gate, evaluate, event_bounds, latency, receiver_intervals, route_gate, voice_continuity, voice_rtts


class GateTests(unittest.TestCase):
    def test_late_latency_recovery_does_not_pass_the_two_second_deadline(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = {"scenario": "latency-voice", "start_host": 0, "seconds": 10, "voice": True, "tcp": False,
                        "clock_offsets": {guest: [0, 0] for guest in ("edge", "hub")}, "events": [
                            {"name": "call lane +100ms", "at_host": 1, "at_guest": {"edge": 1, "hub": 1},
                             "changes": [{"guest": "edge", "lane": 1, "submitted_host": 1, "at_guest": 1}]}]}
            (directory / "scenario.json").write_text(json.dumps(manifest))
            for guest in ("edge", "hub"):
                samples = [[seq, seq * .02 + (.08 if seq * .02 < 3.5 else .02), 80 if seq * .02 < 3.5 else 20] for seq in range(500)]
                (directory / f"{guest}-voice.json").write_text(json.dumps({"epoch": 0, "sent": 500, "samples": samples}))
                (directory / f"{guest}-samples.jsonl").write_text("")
            checks = evaluate(directory, {"phases": {"1": {"idle_p50_ms": [20, 20]}}})
            self.assertTrue(any(check.status == "fail" for check in checks), "median over the rest of the phase hid recovery after the deadline")

    def test_event_is_bounded_by_submissions_and_guest_completions(self):
        event = {"at_host": 120, "at_guest": {"edge": 102, "hub": 103}, "changes": [
            {"guest": "edge", "lane": 1, "submitted_host": 100, "at_guest": 102},
            {"guest": "hub", "lane": 1, "submitted_host": 101, "at_guest": 103}]}
        bounds = event_bounds(event, {"edge": (1, .1), "hub": (-1, .2)}, 100)
        self.assertEqual(bounds.earliest, 0)
        self.assertAlmostEqual(bounds.latest, 4.2)

    def test_latency_boundary_uncertainty_cannot_hide_a_bad_sample(self):
        report = {"samples": [[0, 2.3, 200], [1, 3.12, 20], [2, 3.22, 20]]}
        check = bounded_latency("latency", report, 0, TimeBounds(2, 3), TimeBounds(5, 5), 0, 150, .99, True)
        self.assertEqual(check.status, "inconclusive", "the 200ms sample may be inside the required window")

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
        self.assertEqual(bulk_gate("bulk", intervals, 0, 10, TimeBounds(3, 3), None, .75, True, 0)[0].status, "inconclusive")
        self.assertTrue(all(check.status == "pass" for check in bulk_gate("bulk", intervals, 0, 10, TimeBounds(3, 3), 100, .75, True, 0)))
        self.assertEqual(bulk_gate("bulk", intervals, 0, 10, TimeBounds(3, 3), 200, .75, True, 0)[0].status, "fail")

    def test_partial_deadline_interval_cannot_create_a_false_failure(self):
        intervals = [Interval(.5, 1.5, 100), Interval(1.5, 2.5, 100)]
        checks = bulk_gate("bulk", intervals, 0, 3, TimeBounds(2, 2), 150, .75, False, 0)
        self.assertEqual(checks[0].status, "inconclusive")

    def test_uncertain_receiver_origin_cannot_create_a_deadline_pass(self):
        intervals = [Interval(2, 3, 100)]
        checks = bulk_gate("bulk", intervals, 2, 5, TimeBounds(3, 3), 100, .75, False, 1)
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
