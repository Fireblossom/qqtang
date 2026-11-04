"""Compare sculpture pictures with native constructor/update/draw output."""

import json
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene
from qqtclient.battle.sculpture_animation import StatueAnimation


class SculptureAnimationTest(unittest.TestCase):
    @staticmethod
    def native():
        path = pathlib.Path(__file__).parent / "fixtures/sculpture_animation_v848.jsonl"
        return {row["case"]: row for row in map(json.loads, path.read_text().splitlines())}

    def test_native_deposit_removal_and_animation_checkpoints(self):
        rows = self.native()
        animation = StatueAnimation()
        steps = (
            ("deposit1", 1, 0), ("299", 1, 299), ("300", 1, 1), ("600", 1, 300),
            ("900", 1, 300), ("1999", 1, 1099), ("2000", 1, 1),
            ("deposit2_600", 2, 600), ("remove_to1", 1, 0), ("remove_500", 1, 500),
            ("remove_to0", 0, 20), ("complete16", 4, 0), ("hero300", 4, 300),
            ("hero600", 4, 300), ("complete2000", 4, 1400),
        )
        for case, progress, delta in steps:
            animation.advance(progress, delta, hero_frames=2)
            row = rows[case]
            with self.subTest(case=case):
                self.assertEqual(animation.trigger_left, row["left"])
                stage, hero = row["images"][1:]
                self.assertEqual(animation.stage is None, stage is None)
                if stage:
                    self.assertEqual((animation.stage.frame, animation.stage.remainder), (stage["frame"], stage["remainder"]))
                    self.assertEqual(2 if animation.trigger_left > 0 else 1, stage["action"])
                self.assertEqual(animation.hero is None, hero is None)
                if hero:
                    self.assertEqual((animation.hero.frame, animation.hero.remainder), (hero["frame"], hero["remainder"]))

    def test_native_hero_selection_from_material_order_or_completing_role(self):
        rows = self.native()
        scene = BattleScene.__new__(BattleScene)
        for case, materials, role in (
            ("normal_role1", [12] * 4, 1), ("normal_role9", [12] * 4, 9),
            ("normal_role18", [12] * 4, 18), ("normal_role21", [12] * 4, 21),
            ("material13", [12, 13, 16, 12], 3), ("material14", [12, 14, 13, 12], 3),
            ("material15", [15, 12, 16, 12], 3), ("complete16", [12, 16, 13, 12], 3),
        ):
            with self.subTest(case=case):
                scene.state = {"actors": [{"id": 7, "role": role}]}
                hero = scene._statue_hero(dict(slots=materials, completed_by=7))
                self.assertEqual(hero, f"object/misc/misc{rows[case]['images'][2]['number']}")

    def test_scene_draws_both_complete_hero_frames_on_its_scene_clock(self):
        scene = BattleScene.__new__(BattleScene)
        statue = dict(row=11, col=3, first=True, progress=4, slots=[12, 16, 13, 12], completed_by=7)
        scene.state = {"actors": [{"id": 7, "role": 3}], "sculpture": {"statues": [statue]}}
        scene.statue_animations = {}
        scene.sprites = type("Sprites", (), {"get": staticmethod(lambda path: [None] * 2)})()
        scene.now_ms = 999999  # wall time does not drive the element
        drawn = []
        scene._draw_frames = lambda _, path, frame, x, y: drawn.append((path, frame))
        scene._note_statue_changes(300)
        scene._draw_statues(None, 11)
        self.assertEqual(drawn, [("object/misc/misc221_stand.img", 0),
                                 ("object/misc/misc229_trigger.img", 1),
                                 ("object/misc/misc242_stand.img", 1)])
        scene._note_statue_changes(0)
        self.assertEqual(scene.statue_animations[(11, 3)].trigger_left, 1700)
        scene._note_statue_changes(300)
        drawn.clear()
        scene._draw_statues(None, 11)
        self.assertEqual(drawn[-1], ("object/misc/misc242_stand.img", 0))
