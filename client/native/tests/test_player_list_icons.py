"""The native ui_NotifyShowLevelIcon callback initializes level/kin widgets."""

import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene


class PlayerListIconsTest(unittest.TestCase):
    def test_initialization_uses_room_seats_and_departure_clears_only_that_slot(self):
        scene = BattleScene.__new__(BattleScene)
        calls = []
        scene.runtime = types.SimpleNamespace(namespace={"ui_NotifyShowLevelIcon": lambda *args: calls.append(args)})
        scene.rule = "wrestle"
        scene.players = [dict(id=1, points=15000), dict(id=2, points=35000)]
        scene.seats = {1: 3, 2: 5}
        scene.state = dict(actors=[dict(id=1), dict(id=2)])
        scene.shown_player_icons = None
        scene._update_player_list_icons()
        self.assertEqual(calls, [(0, seat, 0) for seat in range(8)] + [(15000, 3, 1), (35000, 5, 1)])
        calls.clear()
        scene._update_player_list_icons()
        self.assertEqual(calls, [])  # do not restart widget animation each frame
        scene.state["actors"] = [dict(id=2)]
        scene._update_player_list_icons()
        self.assertEqual(calls, [(0, 3, 0)])
        scene.rule = "adventure"
        scene.state["actors"] = []
        calls.clear()
        scene._update_player_list_icons()
        self.assertEqual(calls, [])  # adventure has its own PVE widgets
