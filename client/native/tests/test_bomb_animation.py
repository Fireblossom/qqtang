"""Bubble clocks checked against 005DF518/005DF6FB and actual images."""

import json
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.bomb_animation import BombAnimation
from qqtclient.battle.scene import BattleScene


class BombAnimationTest(unittest.TestCase):
    def test_matches_native_ground_and_landing_checkpoints(self):
        path = pathlib.Path(__file__).parent / "fixtures/bomb_animation_v848.jsonl"
        models = {}
        for row in map(json.loads, path.read_text().splitlines()):
            style, case = row["style"], row["case"]
            if case == "created":
                models[style] = BombAnimation({2: 5, 14: 3}.get(style, 4))
            picture = models[style]
            if case == "landing_frame":
                # Flight existed before this update and completes within it.
                picture.flying = True
                picture.advance(300, False)
            elif case != "created":
                picture.advance({"149": 149, "299": 150, "300": 1, "600": 300, "after_landing": 300}[case])
            with self.subTest(style=style, case=case):
                self.assertEqual((picture.frame, picture.remainder), (row["frame"], row["remainder"]))

    def test_scene_owns_separate_clocks_and_pauses_flight(self):
        scene = BattleScene.__new__(BattleScene)
        scene.bomb_pictures = {}
        scene.sprites = type("Sprites", (), {"get": staticmethod(lambda path: [None] * 4)})()
        scene.state = dict(clock=3000, bombs=[dict(id=1, style=9)])
        scene._update_bomb_pictures(300)
        scene.state["bombs"].append(dict(id=2, style=9))
        scene._update_bomb_pictures(0)
        self.assertEqual([scene.bomb_pictures[i].frame for i in (1, 2)], [1, 0])
        scene.state["kick"] = dict(flights={"1": dict(start=3000, until=4000)})
        scene._update_bomb_pictures(600)
        self.assertEqual([scene.bomb_pictures[i].frame for i in (1, 2)], [1, 2])
        scene.state["clock"] = 4000
        scene._update_bomb_pictures(300)  # actual landing update only sets position/action
        self.assertEqual(scene.bomb_pictures[1].frame, 1)
        scene._update_bomb_pictures(300)
        self.assertEqual(scene.bomb_pictures[1].frame, 2)
        scene.state["bombs"] = []
        scene._update_bomb_pictures(0)
        self.assertEqual(scene.bomb_pictures, {})
