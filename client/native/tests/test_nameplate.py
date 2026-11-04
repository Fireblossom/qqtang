"""Nameplate drawing and F8 edges compared with native VM output."""

import json
import pathlib
import sys
import types
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene, FIELD_ORIGIN
from qqtclient.roomwalk import RoomWalk


class NameplateTest(unittest.TestCase):
    @staticmethod
    def native():
        path = pathlib.Path(__file__).parent / "fixtures/nameplate_v848.jsonl"
        return list(map(json.loads, path.read_text().splitlines()))

    def test_native_f8_edges_keep_room_and_battle_flag_shared(self):
        runtime = types.SimpleNamespace(names_flag=False, namespace={"Win_IsVisible": lambda _: True})
        scene = BattleScene.__new__(BattleScene)
        scene.runtime, scene.names_flag, scene.name_key_down = runtime, False, False
        walk = RoomWalk.__new__(RoomWalk)
        walk.runtime = runtime
        for row in self.native():
            if row["kind"] not in ("keyboard", "keyboard_locked"):
                continue
            event = pygame.KEYDOWN if row["pressed"] else pygame.KEYUP
            scene.handle(pygame.event.Event(event, key=pygame.K_F8))
            self.assertEqual(scene.names_flag, bool(row["flag"]))
            self.assertEqual(walk.names_flag, bool(row["flag"]))

    def test_native_nameplate_colors_byte_width_and_local_arrow(self):
        for row in self.native():
            if row["kind"] != "draw":
                continue
            with self.subTest(case=row["case"], enabled=row["enabled"]):
                local = dict(id=1, team=1, x=100, y=200)
                remote = dict(id=2, team=2 if row["case"] == "enemy" else 1, x=160, y=240)
                scene = BattleScene.__new__(BattleScene)
                scene.local_id, scene.names_flag, scene.name_effect_ms = 1, bool(row["enabled"]), 20
                scene.state = dict(actors=[local, remote])
                scene.names = {2: "Bob_7" if row["case"] == "ascii" else "糖六"}
                scene.text = lambda name, size, color: (name, size, color)
                texts, images, effects = [], [], []

                def blit(glyph, at):
                    name, size, color = glyph
                    texts.append([*at, name, 0xFF000000 | color[0] << 16 | color[1] << 8 | color[2], size])

                surface = types.SimpleNamespace(blit=blit)
                scene._draw_frames = lambda _, path, frame, x, y: images.append((path, x, y))
                scene.effect_player = types.SimpleNamespace(draw=lambda *args: effects.append(args[1:]))
                actor = local if row["case"] == "local" else remote
                scene._draw_nameplate(surface, actor, FIELD_ORIGIN[0] + actor["x"], FIELD_ORIGIN[1] + actor["y"])
                # Existing native viewport (8,21) vs host field (10,27).
                expected_texts = [[x + 2, y + 6, name, color, size] for x, y, name, color, size in row["texts"]]
                expected_images = [(f"object/misc/misc{r['number']}_stand.img", r["position"][0] + 2, r["position"][1] + 6)
                                   for r in row["images"]]
                self.assertEqual(texts, expected_texts)
                self.assertEqual(images, expected_images)
                if row["effects"]:
                    update = next(r for r in self.native() if r["kind"] == "update" and r["case"] == "local")
                    _, x, y = update["effects"][0]
                    self.assertEqual(effects, [("playerArrow.eff", 20, x + 2, y + 6)])
                else:
                    self.assertEqual(effects, [])
