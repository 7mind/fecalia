"""Behavioral-Effectual-GoodCommunication: descheduling must appear in timing evidence."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest


class ObserverTest(unittest.TestCase):
    def test_descheduling_is_visible_in_wake_delay(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "timing.jsonl"
            observer = subprocess.Popen([sys.executable, str(Path(__file__).with_name("observe.py")),
                "--seconds", "1", "--output", str(output), "--pid", str(os.getpid())])
            try:
                deadline = time.monotonic() + 5
                while not output.exists() or output.stat().st_size == 0:
                    if time.monotonic() >= deadline or observer.poll() is not None:
                        self.fail("observer did not produce timing evidence")
                    time.sleep(0.01)
                observer.send_signal(signal.SIGSTOP)
                time.sleep(0.2)
                observer.send_signal(signal.SIGCONT)
                self.assertEqual(observer.wait(timeout=5), 0)
            finally:
                if observer.poll() is None:
                    observer.send_signal(signal.SIGCONT)
                    observer.terminate()
                    observer.wait(timeout=5)
            samples = [json.loads(line) for line in output.read_text().splitlines()]
            self.assertGreaterEqual(max(sample["wake_delay_seconds"] for sample in samples), 0.15)
            self.assertTrue(all(str(os.getpid()) in sample["threads"] for sample in samples))

    def test_missing_process_cannot_produce_valid_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("observe.py")),
                "--seconds", "0.2", "--output", str(Path(directory) / "timing.jsonl"),
                "--pid", str(2**31-1)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("FileNotFoundError", result.stderr)


if __name__ == "__main__":
    unittest.main()
