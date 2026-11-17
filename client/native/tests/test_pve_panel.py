"""Rule 12's PVE panel callbacks: 0x005BA327 (a player leaves) and
0x005BA3CB (the local player places a bubble)."""

import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene, base_item_ring_speed


class PVEPanelTest(unittest.TestCase):
    @staticmethod
    def scene(adventure):
        scene = BattleScene.__new__(BattleScene)
        scene.state = dict(adventure=adventure)
        scene.shown_pve_hp, scene.shown_pve_bosses = {}, ()
        scene.shown_pve_left, scene.shown_pve_placed = set(), 0
        calls = []
        names = ("ui_UpdatePVEPlayerLife", "ui_InitPVEBoss", "ui_UpdatePVEBossLife", "ui_DisposePVEPlayerLeave",
                 "ui_SetBaseItemTimeCur", "ui_SetBaseItemTimeSpeed")
        scene.runtime = types.SimpleNamespace(namespace={
            name: (lambda *args, name=name: calls.append((name, args))) for name in names})
        return scene, calls

    def test_a_leaver_is_disposed_once(self):
        scene, calls = self.scene(dict(players={}, npcs=[], left=[2]))
        scene._update_adventure()
        self.assertEqual(calls, [("ui_DisposePVEPlayerLeave", (2,))])
        calls.clear()
        scene._update_adventure()
        self.assertEqual(calls, [])
        scene.state["adventure"]["left"] = [2, 3]
        scene._update_adventure()
        self.assertEqual(calls, [("ui_DisposePVEPlayerLeave", (3,))])

    def test_each_own_bubble_restarts_the_ring(self):
        scene, calls = self.scene(dict(players={}, npcs=[], resume_ms=2000))
        scene._update_adventure()
        self.assertEqual(calls, [])  # InitBaseItemTime's 360 stays until a bubble
        scene.state["adventure"]["placed"] = 1
        scene._update_adventure()
        self.assertEqual(calls, [("ui_SetBaseItemTimeCur", (0.0,)), ("ui_SetBaseItemTimeSpeed", (50.0,))])
        calls.clear()
        scene._update_adventure()
        self.assertEqual(calls, [])
        scene.state["adventure"]["placed"] = 2
        scene._update_adventure()
        self.assertEqual([name for name, _ in calls], ["ui_SetBaseItemTimeCur", "ui_SetBaseItemTimeSpeed"])

    def test_ring_speed_is_100_over_the_refill_seconds(self):
        self.assertEqual(base_item_ring_speed(2000), 50.0)
        self.assertEqual(base_item_ring_speed(1000), 100.0)
        self.assertAlmostEqual(base_item_ring_speed(800), 125.0)
        # The original's sub-0.5 s branch yields 50; fixed to the 0.5 s floor.
        self.assertEqual(base_item_ring_speed(400), 200.0)
        self.assertEqual(base_item_ring_speed(0), 200.0)


if __name__ == "__main__":
    unittest.main()
