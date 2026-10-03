"""Behavioral-Blackbox-Atomic application-time evidence invariants."""

from concurrent.futures import Future
import unittest

from adapt import completed_changes


class TimelineTests(unittest.TestCase):
    def test_both_lane_changes_retain_four_application_times(self):
        applied = []
        for guest in ("edge", "hub"):
            for lane in (1, 2):
                future = Future()
                future.set_result(100 + lane)
                applied.append((guest, lane, 99, future))
        completed = completed_changes(applied)
        self.assertEqual(len(completed), 4, "application times collapsed distinct lane changes by guest")
        self.assertEqual({(item["guest"], item["lane"]) for item in completed}, {("edge", 1), ("edge", 2), ("hub", 1), ("hub", 2)})
        self.assertTrue(all(item["submitted_host"] == 99 and item["at_guest"] == 100 + item["lane"] for item in completed))


if __name__ == "__main__":
    unittest.main()
