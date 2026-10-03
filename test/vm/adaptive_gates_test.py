"""Behavioral-Blackbox-Atomic checks of the policy acceptance evaluator."""

import json
from pathlib import Path
import tempfile
import unittest

from adaptive_gates import Interval, TimeBounds, bounded_latency, bulk_gate, egress_loss_gate, evaluate, event_bounds, latency, receiver_intervals, route_gate, voice_continuity, voice_rtts


class GateTests(unittest.TestCase):
    def test_cold_deadline_starts_with_the_transfer_instead_of_dispatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = {"scenario": "cold", "start_host": 0, "seconds": 10, "voice": False, "tcp": True,
                        "profile": {guest: {"1": {"rate": 1}} for guest in ("edge", "hub")},
                        "clock_offsets": {guest: [0, 0] for guest in ("edge", "hub")}, "events": []}
            (directory / "scenario.json").write_text(json.dumps(manifest))
            tcp = {"start": {"timestamp": {"timemillisecs": 2000}}, "test_started_guest": 2,
                   "intervals": [{"streams": [{"sender": False, "omitted": False, "start": second, "end": second + 1,
                                               "bytes": 100000 if second >= 6 else 0}]} for second in range(8)]}
            (directory / "tcp.json").write_text(json.dumps({**tcp, "server_output_json": tcp}))
            for guest in ("edge", "hub"):
                (directory / f"{guest}-samples.jsonl").write_text("")
            checks = evaluate(directory, {})
            self.assertEqual([check.status for check in checks], ["pass", "pass"],
                             "delivery reached 60% in the transfer's seventh second; dispatch was two seconds earlier")
            for interval in tcp["intervals"]:
                interval["streams"][0]["bytes"] = 0
            (directory / "tcp.json").write_text(json.dumps({**tcp, "server_output_json": tcp}))
            self.assertEqual([check.status for check in evaluate(directory, {})], ["fail", "fail"])
            tcp.pop("test_started_guest")
            (directory / "tcp.json").write_text(json.dumps({**tcp, "server_output_json": tcp}))
            self.assertTrue(all(check.status == "inconclusive" for check in evaluate(directory, {})))

    def test_egress_loss_does_not_count_old_backlog_as_newly_accepted_bytes(self):
        samples = [{"bounds": TimeBounds(at, at), "tc": {"eth1": {
            "qdisc": [{"kind": "htb", "root": True, "handle": "1:", "bytes": delivered, "backlog": backlog}],
            "filter": [{"options": {"actions": [{"kind": "police", "index": 1, "stats": {"bytes": offered}}]}}]}}}
            for at, offered, delivered, backlog in ((53, 1000, 900, 100), (65, 1100, 1050, 50))]
        check = egress_loss_gate("loss", samples, TimeBounds(53, 53), TimeBounds(65, 65), "eth1")
        self.assertEqual(check.status, "pass", "an old queue drained while only 100 new bytes were offered")

    def test_plan_down_loss_uses_phase_bounded_wire_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = {"scenario": "plan", "start_host": 0, "seconds": 65, "voice": False, "tcp": True,
                        "clock_offsets": {guest: [0, 0] for guest in ("edge", "hub")}, "events": [
                            {"name": name, "changes": [{"guest": guest, "lane": 1, "submitted_host": at, "at_guest": at} for guest in ("edge", "hub")]}
                            for name, at in (("satellite roam", 20), ("satellite standby", 50))]}
            (directory / "scenario.json").write_text(json.dumps(manifest))
            tcp = {"start": {"timestamp": {"timemillisecs": 0}}, "test_started_guest": 0,
                   "intervals": [{"streams": [{"sender": False, "omitted": False, "start": second, "end": second + 1, "bytes": 100}]} for second in range(65)]}
            (directory / "tcp.json").write_text(json.dumps({**tcp, "server_output_json": tcp}))

            def loss_checks(points):
                samples = []
                for at, offered, accepted, index in points:
                    samples.append(json.dumps({"t": at, "t_complete": at, "m": {}, "tc": {"eth1": {
                        "qdisc": [{"kind": "htb", "root": True, "handle": "1:", "bytes": 1000000000 + accepted, "backlog": 0, "drops": 1000},
                                  {"kind": "netem", "parent": "1:10", "bytes": 1000000000 + accepted, "backlog": 0, "drops": 1000}],
                        "filter": [{"options": {"actions": [{"kind": "police", "index": index, "stats": {"bytes": offered, "drops": 1000}}]}}]}}}))
                for guest in ("edge", "hub"):
                    (directory / f"{guest}-samples.jsonl").write_text("\n".join(samples))
                return [check.status for check in evaluate(directory, {"phases": {"1": {"payload_bps": [100, 100]}}}) if check.name.endswith("/lane-loss")]

            self.assertEqual(loss_checks([(52.8, 0, 0, 1), (53.2, 100, 100, 1), (64.8, 120000, 120000, 1), (65.2, 120100, 120100, 1)]), ["pass", "pass"],
                             "root and child drop counts do not establish the phase's byte loss")
            self.assertEqual(loss_checks([(53, 0, 0, 1), (65, 120000, 114000, 1)]), ["fail", "fail"], "under-five-percent is strict")
            self.assertEqual(loss_checks([(53, 0, 0, 1), (65, 120000, 118000, 1)]), ["pass", "pass"])
            self.assertEqual(loss_checks([(52.8, 0, 0, 1), (53.2, 10000, 10000, 1), (64.8, 110000, 110000, 1), (65.2, 120000, 120000, 1)]), ["inconclusive", "inconclusive"],
                             "unobserved boundary traffic cannot be assumed delivered")
            self.assertEqual(loss_checks([(53, 0, 0, 1), (65, 120000, 120000, 2)]), ["inconclusive", "inconclusive"])
            self.assertEqual(loss_checks([(53, 100000, 100000, 1), (65, 0, 0, 1)]), ["inconclusive", "inconclusive"])
            self.assertEqual(loss_checks([(53, 0, 0, 1), (65, 0, 0, 1)]), ["inconclusive", "inconclusive"])

    def test_return_lane_requires_receipts_bounded_by_the_two_second_deadline(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = {"scenario": "blackout", "start_host": 0, "seconds": 16, "voice": False, "tcp": True,
                        "clock_offsets": {guest: [0, 0] for guest in ("edge", "hub")}, "events": [
                            {"name": "mobile back", "changes": [
                                {"guest": guest, "lane": 2, "submitted_host": 10, "at_guest": 10} for guest in ("edge", "hub")]}]}
            (directory / "scenario.json").write_text(json.dumps(manifest))
            tcp = {"start": {"timestamp": {"timemillisecs": 0}}, "test_started_guest": 0,
                   "intervals": [{"streams": [{"sender": False, "omitted": False, "start": second, "end": second + 1, "bytes": 100}]} for second in range(16)]}
            (directory / "tcp.json").write_text(json.dumps({**tcp, "server_output_json": tcp}))
            references = {"phases": {"1": {"payload_bps": [100, 100]}}}

            def receipt_checks(points, completion=True, received=True):
                for guest, lane in (("edge", "256"), ("hub", "1")):
                    samples = []
                    for started, finished, count in points:
                        sample = {"t": started, "m": {
                            f'wanbond_adaptive_bulk_original_packets_total{{peer="other",lane="{lane}"}}': 1000 + started,
                            f'wanbond_adaptive_received_bulk_packets_total{{peer="other",lane="{lane}"}}': count}}
                        if completion:
                            sample["t_complete"] = finished
                        if not received:
                            sample["m"].pop(f'wanbond_adaptive_received_bulk_packets_total{{peer="other",lane="{lane}"}}')
                        samples.append(json.dumps(sample))
                    (directory / f"{guest}-samples.jsonl").write_text("\n".join(samples))
                return [check.status for check in evaluate(directory, references) if check.name.endswith("/lane-bulk")]

            self.assertEqual(receipt_checks([(10.1, 10.11, 100), (11.9, 11.91, 101)]), ["pass", "pass"],
                             "physical receipt before the deadline was not recognized")
            self.assertEqual(receipt_checks([(10.1, 10.11, 100), (11.9, 12.1, 101)]), ["inconclusive", "inconclusive"],
                             "HTTP read spanning the deadline cannot prove a timely receipt")
            self.assertEqual(receipt_checks([(9.8, 9.9, 100), (12.1, 12.2, 100)]), ["fail", "fail"],
                             "submissions cannot replace missing physical bulk receipts")
            self.assertEqual(receipt_checks([(9.8, 9.9, 100), (12.1, 12.2, 101)]), ["inconclusive", "inconclusive"])
            self.assertEqual(receipt_checks([(10.1, 10.11, 100), (11.9, 11.91, 101)], completion=False), ["inconclusive", "inconclusive"])
            self.assertEqual(receipt_checks([(10.1, 10.11, 100), (11.9, 11.91, 1)]), ["inconclusive", "inconclusive"])
            self.assertEqual(receipt_checks([(10.1, 10.11, 100), (11.9, 11.91, 101)], received=False), ["inconclusive", "inconclusive"])

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
