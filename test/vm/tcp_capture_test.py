"""Shared event-stream contract over an in-memory source and a real pipe."""

import io
import json
import subprocess
import sys
import unittest

from tcp_capture import capture


class CaptureTests(unittest.TestCase):
    def sources(self, events):
        encoded = "".join(json.dumps(event) + "\n" for event in events)
        yield "memory", io.StringIO(encoded)
        with subprocess.Popen([sys.executable, "-c", "import sys; sys.stdout.write(sys.argv[1])", encoded], stdout=subprocess.PIPE, text=True) as process:
            yield "process", process.stdout
        self.assertEqual(process.returncode, 0)

    def test_actual_start_is_independent_of_connection_timestamp(self):
        events = [{"event": "start", "data": {"timestamp": {"timemillisecs": 100000}}},
                  {"event": "interval", "data": {"bytes": 100}}, {"event": "end", "data": {}}]
        for name, source in self.sources(events):
            with self.subTest(source=name):
                records = []
                ticks = iter((101.25, 102.25, 102.26))
                report = capture(source, lambda: next(ticks), lambda at, event: records.append((at, event)))
                self.assertEqual(report["test_started_guest"], 101.25)
                self.assertEqual(report["start"]["timestamp"]["timemillisecs"], 100000)
                self.assertEqual(report["intervals"], [{"bytes": 100}])
                self.assertEqual(records[0], (101.25, "start"))

    def test_missing_start_and_iperf_errors_fail(self):
        for events, error, message in (([{"event": "interval", "data": {}}], ValueError, "outside started test"),
                                       ([{"event": "error", "data": "connection refused"}], RuntimeError, "connection refused")):
            for name, source in self.sources(events):
                with self.subTest(source=name), self.assertRaisesRegex(error, message):
                    capture(source, lambda: 100, lambda at, event: None)


if __name__ == "__main__":
    unittest.main()
