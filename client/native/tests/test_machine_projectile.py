"""Positions and transition frames captured from native 006089C5."""

import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.machine_projectile import MachineProjectile
from qqtclient.battle.scene import BattleScene


class MachineProjectileTest(unittest.TestCase):
    def test_native_left_path_and_same_frame_handover(self):
        p = MachineProjectile(6, 3, True)
        p.advance(48699, 48000)
        self.assertIsNone(p.picture(48699))
        p.advance(48700, 48000)
        self.assertEqual(p.first.x, 161.0)
        self.assertEqual(p.first.y, 179.3333282470703)
        for _ in range(118):
            p.advance(48700, 48000)  # native movement is per call, not dt
        self.assertFalse(p.first.active)
        self.assertTrue(p.second.active)
        self.assertEqual((p.first.x, p.first.y), (279.0, 100.6663818359375))
        self.assertEqual((p.second.x, p.second.y), (280.0, 98.0))
        self.assertEqual(p.picture(48700)[0], 166)
        for _ in range(49):
            p.advance(48700, 48000)
        self.assertIsNone(p.picture(48700))
        self.assertEqual(p.second.y, 0.0)

    def test_native_right_and_above_field_paths(self):
        for row, col, first, count in ((6, 10, False, 158), (3, 3, True, 128), (9, 10, False, 218)):
            p = MachineProjectile(row, col, first)
            for _ in range(count - 1):
                p.advance(1000, 1)
            self.assertTrue(p.second.active)
            p.advance(1000, 1)
            self.assertFalse(p.second.active)
            self.assertEqual((p.second.x, p.second.y), (280.0, 0.0))

    def test_new_launch_keeps_animation_phase(self):
        p = MachineProjectile(6, 3, True)
        for _ in range(168):
            p.advance(1000, 1)
        old = (p.first.animation_ms, p.second.animation_ms)
        p.advance(96000, 96000)
        self.assertEqual((p.first.animation_ms, p.second.animation_ms), old)
        self.assertEqual((p.first.x, p.first.y), (160.0, 180.0))
        self.assertIsNone(p.picture(96000))

    def test_native_animation_period_is_300ms(self):
        p = MachineProjectile(6, 3, True)
        for _ in range(29):
            p.advance(1000, 1)
        self.assertEqual(p.picture(1000)[1], 0)
        p.advance(1000, 1)
        self.assertEqual(p.picture(1000)[1], 1)
        for _ in range(89):
            p.advance(1000, 1)
        self.assertEqual(p.picture(1000)[:2], (166, 0))
        for _ in range(14):
            p.advance(1000, 1)
        self.assertEqual(p.picture(1000)[:2], (166, 1))

    def test_scene_draws_native_anchor_and_retires_destroyed_machine(self):
        scene = BattleScene.__new__(BattleScene)
        gate = dict(row=6, col=3, first=True, fired=48000, ready=False, team=1)
        scene.state = dict(clock=48700, actors=[], machine=dict(gates=[gate]))
        scene.local_id, scene.machine_projectiles = 1, {}
        calls = []
        scene._draw_frames = lambda *args: calls.append(args)
        scene._update_machine_projectiles()
        scene._draw_machine_gates(None, 6)
        self.assertEqual(calls[-1][1:], ("object/misc/misc165_stand.img", 0, 151, 186))
        gate["destroyed"] = True
        scene._update_machine_projectiles()
        self.assertEqual(scene.machine_projectiles, {})
