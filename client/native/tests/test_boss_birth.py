"""Default model loading differs from explicitly requesting a birth image."""

import json
import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene, NPC_SHADOW


class BossBirthDrawingTest(unittest.TestCase):
    def test_default_birth_keeps_shadow_and_hp_without_forcing_the_asset(self):
        scene = BattleScene.__new__(BattleScene)
        boss = dict(id=30001, role=32, x=100, y=100, facing=1, moving=False,
                    hp=1, protected_until=0, born=0, birth_age=400, birth_mode=9)
        scene.state = {"clock": 3000, "boss": {"actors": [boss]}}
        scene.map = types.SimpleNamespace(height=13)
        scene.walk_started = {}
        images, actors = [], []
        scene._draw_frames = lambda _, path, frame, x, y: images.append((path, frame, x, y))
        scene._draw_actor = lambda _, record: actors.append(record)
        scene._draw_bosses(None, 2)
        self.assertEqual(images[0], (NPC_SHADOW, 0, 90, 122))
        self.assertEqual(actors, [])
        self.assertTrue(any("misc196" in path for path, *_ in images))
        self.assertFalse(any("_birth.img" in path for path, *_ in images))
        boss.update(birth_mode=1, birth_age=20)
        scene._draw_bosses(None, 2)
        self.assertEqual(len(actors), 1)

    def test_constructor_and_draw_oracle_distinguishes_explicit_loading(self):
        path = pathlib.Path(__file__).parent / "fixtures/npc_picture_v848.jsonl"
        defaults, explicit = [], []
        for row in map(json.loads, path.read_text().splitlines()):
            if row["operation"] == "birth":
                self.assertFalse(row["data_present"])
                self.assertEqual(row["draws"], [NPC_SHADOW])
                defaults.append(row["role"])
            elif row["kind"] == "explicit" and row["image"]["exists"]:
                self.assertIn(row["image"]["path"], row["draws"])
                explicit.append(row["role"])
        self.assertEqual(len(defaults), 9)
        self.assertEqual(set(explicit), {24, 28, 32, 34, 35, 36})
