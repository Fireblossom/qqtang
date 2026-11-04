"""Machine component frames captured from 005EF7DE + 005EF91F + 0042A680."""

import json
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.machine_projectile import FrameLoop, MachineGateAnimation


class MachineAnimationTest(unittest.TestCase):
    def test_matches_native_component_checkpoints(self):
        path = pathlib.Path(__file__).parent / "fixtures/machine_animation_v848.jsonl"
        expected = {row["clock"]: row for row in map(json.loads, path.read_text().splitlines())}
        machine = MachineGateAnimation(6, 3, True)
        inputs = [(clock, 20) for clock in range(3000, 96841, 20)] + [(200000, 4000), (204000, 4000)]
        checked = set()
        for clock, delta in inputs:
            fired = 0 if clock < 48420 else 48420 if clock < 96840 else 96840
            machine.advance(clock, fired, delta, clock - fired >= 48000, True)
            if clock not in expected:
                continue
            row = expected[clock]
            with self.subTest(clock=clock):
                self.assertEqual({name: [loop.frame, loop.remainder] for name, loop in machine.loops.items()}, row["frames"])
                drawn = [name for name, _ in machine.body_layers()] + (["arrow"] if machine.arrow_visible else [])
                self.assertEqual(drawn, row["draws"])
                self.assertEqual(machine.draw_clock, row["stored_clock"])
                self.assertEqual(machine.is_ready, bool(row["ready"]))
                self.assertEqual(machine.arrow_visible, bool(row["arrow"]))
            checked.add(clock)
        self.assertEqual(checked, expected.keys())

    def test_other_team_does_not_advance_local_arrow(self):
        machine = MachineGateAnimation(6, 10, False)
        machine.advance(48000, 0, 20, True, False)
        machine.advance(48300, 0, 300, True, False)
        self.assertFalse(machine.arrow_visible)
        self.assertEqual(machine.loops["arrow"].frame, 0)
        self.assertEqual(machine.loops["ready"].frame, 1)

    def test_large_delta_resets_frame_without_preserving_overshoot(self):
        loop = FrameLoop(12, frame=2, remainder=240)
        loop.advance(4000)
        self.assertEqual((loop.frame, loop.remainder), (0, 40))
