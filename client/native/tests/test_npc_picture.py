"""Default NPC picture selection compared with the complete native model path."""

import json
import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene, NPC_SHADOW


class NPCPictureDrawingTest(unittest.TestCase):
    def test_box_npc_draws_match_native_body_availability_frames_and_shadow(self):
        rows = map(json.loads, (pathlib.Path(__file__).parent / "fixtures/npc_picture_v848.jsonl").read_text().splitlines())
        checked = 0
        for row in rows:
            if row["kind"] != "event" or row["role"] not in (25, 26, 27):
                continue
            action, clock = row["action"], row["clock"]
            facing = {0: 2, 1: 1, 2: 4, 3: 3}[row["facing"]]
            npc = dict(id=20001, role=row["role"], team=7, x=220, y=260, facing=facing,
                       moving=action == 4, trapped=action == 2, dead=False,
                       picture_action=action, picture_clock=clock, picture_facing=facing)
            scene = BattleScene.__new__(BattleScene)
            scene.state = {"clock": 90000, "box": {"npc": {"actors": [npc]}}}
            scene.map = types.SimpleNamespace(height=13)
            scene.rule = "box"
            scene.walk_started = {}
            scene.eliminated_at, scene.trapped_at, scene.wear = {}, {}, {}
            scene.now_ms = 999999  # does not drive the body picture
            scene.practice_emotion = None
            images, avatars = [], []
            scene._draw_frames = lambda _, path, frame, x, y: images.append((path, frame, x, y))
            scene._draw_flash = scene._draw_nameplate = scene._draw_syrup = lambda *args: None
            def avatar(role, team, action="stand", direction=3, frame=0, **kwargs):
                avatars.append((action, frame))
                return None
            scene.avatars = types.SimpleNamespace(get=avatar)
            scene._draw_boxes(None, 6)
            self.assertEqual(images, [(NPC_SHADOW, 0, 210, 282)])
            self.assertEqual([b-a for a, b in zip(row["cloth_position"], row["shadow_position"])], [30, 59])
            body = [path for path in row["draws"] if path != NPC_SHADOW]
            if body:
                self.assertEqual(len(avatars), 1, row)
                name, frame = avatars[0]
                self.assertEqual(name, {1: "stand", 2: "trigger", 4: "walk"}[action])
                self.assertEqual(frame % row["image"]["frames"], row["frame"], row)
            else:
                self.assertEqual(avatars, [], row)
            checked += 1
        self.assertEqual(checked, 42)
