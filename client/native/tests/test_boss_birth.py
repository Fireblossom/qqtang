"""Boss birth uses actor age/mode even while engine time is parked at Ready."""

import pathlib
import json
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene


class BossBirthDrawingTest(unittest.TestCase):
    def test_birth_frames_use_actor_age_and_hold_until_mode_changes(self):
        scene = BattleScene.__new__(BattleScene)
        boss = dict(id=30001, role=32, x=100, y=100, facing=1, moving=False,
                    hp=1, protected_until=0, born=60, birth_age=400, birth_mode=9)
        scene.state = {"clock": 3000, "boss": {"actors": [boss]}}
        scene.map = types.SimpleNamespace(height=13)
        scene.avatars = types.SimpleNamespace(parts=lambda role, action: {"cloth": 32})
        scene.sprites = types.SimpleNamespace(get=lambda path: [None] * 8)
        scene.walk_started = {}
        images, actors = [], []
        scene._draw_frames = lambda _, path, frame, x, y: images.append((path, frame))
        scene._draw_actor = lambda _, record: actors.append(record)
        scene._draw_bosses(None, 2)
        self.assertEqual(images[0], ("object/cloth/cloth32_birth.img", 2))
        self.assertEqual(actors, [])

        # A delayed state transition must keep the last birth pose, rather
        # than switching to stand solely because the animation window ended.
        boss["birth_age"] = 3300
        images.clear()
        scene._draw_bosses(None, 2)
        self.assertEqual(images[0], ("object/cloth/cloth32_birth.img", 7))
        self.assertEqual(actors, [])

        # The greeting shortcut can leave birth on the very first actor frame.
        boss.update(birth_mode=1, birth_age=20)
        images.clear()
        scene._draw_bosses(None, 2)
        self.assertFalse(any("_birth.img" in path for path, frame in images))
        self.assertEqual(len(actors), 1)

    def test_birth_drawing_matches_original_part_update_frames(self):
        path = pathlib.Path(__file__).parent / "fixtures/boss_birth_animation_v848.jsonl"
        configs = {}
        checked = 0
        for line in path.read_text().splitlines():
            row = json.loads(line)
            if row["kind"] == "config":
                self.assertEqual((row["period"], row["action"], row["facing"], row["directions"]), (200, 14, 270, 1))
                configs[row["role"]] = row
                continue
            config = configs[row["role"]]
            cloth = int(config["path"].rsplit("cloth", 1)[1].split("_", 1)[0])
            scene = BattleScene.__new__(BattleScene)
            boss = dict(id=30001, role=row["role"], x=100, y=100, facing=1, moving=False,
                        hp=1, protected_until=0, born=0, birth_age=row["elapsed"], birth_mode=9)
            scene.state = {"clock": 3000, "boss": {"actors": [boss]}}
            scene.map = types.SimpleNamespace(height=13)
            scene.avatars = types.SimpleNamespace(parts=lambda role, action: {"cloth": cloth})
            scene.sprites = types.SimpleNamespace(get=lambda path: [None] * config["frames"])
            images = []
            scene._draw_frames = lambda _, path, frame, x, y: images.append((path, frame))
            scene._draw_bosses(None, 2)
            self.assertEqual(images[0], (config["path"], row["frame"]), (row["role"], row["case"], row["elapsed"]))
            checked += 1
        self.assertEqual((len(configs), checked), (6, 66))
