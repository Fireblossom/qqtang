"""Prepared prop masks, checked against 005DAC82's captured script calls."""

import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle import prepared
from qqtclient.battle.scene import BattleScene


class PreparedCooldownTest(unittest.TestCase):
    def test_native_mask_golden_parameters(self):
        base = dict(initial=10000, interval=5000, last_use=0, allowed=True)
        self.assertEqual(prepared.mask(base, 3000), (108.0, 36.0))
        self.assertEqual(prepared.mask({**base, "allowed": False}, 3000), (0.0, 0.0))
        used = dict(initial=0, interval=5000, last_use=10000, allowed=True)
        angle, speed = prepared.mask(used, 12000)
        self.assertEqual(angle, 144.0)
        self.assertAlmostEqual(speed, 66.66666666666667)
        self.assertEqual(prepared.mask(used, 15000)[0], 360.0)
        self.assertFalse(prepared.ready(used, 14999))
        self.assertTrue(prepared.ready(used, 15000))
        self.assertEqual(prepared.mask(None, 1000), (360.0, 0.0))

    @staticmethod
    def scene():
        scene = BattleScene.__new__(BattleScene)
        scene.local_id = 1
        scene.state = dict(clock=3000, actors=[dict(id=1, state=0, held=[[64, 1]])],
                           prepared=[dict(item=20020, count=3, cooldown=dict(initial=20000, interval=15000, last_use=0, allowed=True))])
        scene.shown_slots, scene.shown_masks = None, {}
        calls = []
        scene.runtime = types.SimpleNamespace(namespace={
            name: (lambda *args, name=name: calls.append((name, args)))
            for name in ("Win_SetImg", "Win_SetText", "ui_SetItemTimeCur", "ui_SetItemTimeSpeed")})
        return scene, calls

    def test_masks_advance_without_an_inventory_change(self):
        scene, calls = self.scene()
        scene._update_status_bar()
        self.assertEqual(scene.shown_masks[0], (54.0, 18.0))
        calls.clear()
        scene.state["clock"] = 10000
        scene._update_status_bar()
        self.assertEqual(scene.shown_masks[0], (180.0, 18.0))
        self.assertEqual({name for name, _ in calls}, {"ui_SetItemTimeCur", "ui_SetItemTimeSpeed"})
        scene.state["clock"] = 20000
        scene._update_status_bar()
        self.assertEqual(scene.shown_masks[0], (360.0, 0.0))
        scene.state["prepared"][0]["count"] = 0
        scene._update_status_bar()
        self.assertEqual(scene.shown_masks[0], (360.0, 0.0))

    def test_ctrl_skips_unavailable_prepared_prop(self):
        scene, _ = self.scene()
        self.assertEqual(scene._smart_use(), 64)
        scene.state["clock"] = 20000
        self.assertEqual(scene._smart_use(), 20020)
        scene.state["prepared"][0]["cooldown"]["last_use"] = 20000
        self.assertEqual(scene._smart_use(), 64)
