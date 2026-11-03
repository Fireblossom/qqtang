"""Battle map reading and drawing decisions; no client files or sidecar needed."""

from __future__ import annotations

import pathlib
import struct
import sys
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from qqtclient.battle import mapfile  # noqa: E402
from qqtclient.battle.practice import practice_players  # noqa: E402
from qqtclient.battle.scene import (ACTOR_TRAPPED, BUN_HOUSES, BUN_ICONS, FLASH_IMAGE, NEEDLE_ACTION, TREASURE_BADGE,  # noqa: E402
                                   TREASURE_DIGITS, BattleScene, Explosion, effect_frame, flame_image, flight_position,
                                   player_title)
from qqtclient.battle.sidecar import ACTOR_ELIMINATED, EVENT_ACTOR_RESPAWNED  # noqa: E402


class FakeResources:
    def __init__(self, files: dict[str, bytes]):
        self.files = files

    def read(self, path: str) -> bytes | None:
        return self.files.get(path)


class MapFileTest(unittest.TestCase):
    def test_version3_layers(self) -> None:
        cells = 15 * 13
        layers = [[0] * cells, [3004] + [0] * (cells - 1), [3002] * cells]
        data = struct.pack("<III", 3, 1, 8) + b"".join(struct.pack(f"<{cells}i", *layer) for layer in layers) + bytes(20)
        parsed = mapfile.parse_map(data)
        self.assertEqual((parsed.version, parsed.rule, parsed.width, parsed.height), (3, 1, 15, 13))
        self.assertEqual(parsed.element(mapfile.LAYER_WALL, 0, 0), 3004)
        self.assertEqual(parsed.element(mapfile.LAYER_FLOOR, 12, 14), 3002)

    def test_version4_size(self) -> None:
        data = struct.pack("<IIIII", 4, 2, 2, 3, 2) + struct.pack("<18i", *range(18))
        parsed = mapfile.parse_map(data)
        self.assertEqual((parsed.width, parsed.height), (3, 2))
        self.assertEqual(parsed.element(2, 1, 2), 17)

    def test_truncated_map(self) -> None:
        with self.assertRaises(ValueError):
            mapfile.parse_map(struct.pack("<III", 3, 1, 8) + bytes(10))

    def test_descriptions_and_elements(self) -> None:
        desc = "mapDesc = [\n(211, 'town', '景阳岗', 15, 13, 8, 'town11_8.map', 'town11_8.img', 'town.ogg', 0, '', 1, 0),\n]\n"
        elements = (
            "class QQTMapElem:\n    offset = (0, 0)\n\n"
            "class QQTMapElem3004(QQTMapElem):\n    LifeTime = 1\n    offset = (2, 13)\n    imageID = 3004\n\n"
            "class QQTMapElem3005(QQTMapElem):\n    imageID = 3005\n    canHide = 1\n"
        )
        resources = FakeResources({"map/mapDesc.py": desc.encode("gbk"), "object/mapElem/mapElem.py": elements.encode()})
        self.assertEqual(mapfile.load_descriptions(resources)[211].file_name, "town11_8.map")
        loaded = mapfile.load_elements(resources)
        self.assertEqual((loaded[3004].offset, loaded[3004].life_time), ((2, 13), 1))
        self.assertTrue(loaded[3005].can_hide)

    def test_element_image(self) -> None:
        self.assertEqual(mapfile.element_image(3004), "object/mapElem/town/elem4_stand.img")
        self.assertEqual(mapfile.element_image(17105, "die"), "object/mapElem/common/elem105_die.img")
        self.assertIsNone(mapfile.element_image(99001))


class FlameTest(unittest.TestCase):
    def test_flame_images(self) -> None:
        blast = Explosion(5, 5, (3, 7, 4, 7))
        self.assertEqual(flame_image(blast, 5, 5), 1)
        self.assertEqual(flame_image(blast, 5, 6), 2)  # right arm
        self.assertEqual(flame_image(blast, 5, 7), 6)  # right end
        self.assertEqual(flame_image(blast, 4, 5), 3)  # up arm
        self.assertEqual(flame_image(blast, 3, 5), 7)  # up end
        self.assertEqual(flame_image(blast, 5, 4), 8)  # left end
        self.assertEqual(flame_image(blast, 7, 5), 9)  # down end
        self.assertEqual(flame_image(None, 1, 1), 1)


class HeldItemTest(unittest.TestCase):
    @staticmethod
    def scene(held: list[list[int]], state: int = 0) -> BattleScene:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id = 1
        scene.state = {"actors": [{"id": 1, "state": state, "held": held}]}
        return scene

    def test_ctrl_takes_the_first_usable_slot(self) -> None:
        scene = self.scene([[NEEDLE_ACTION, 1], [64, 1], [0, 0], [44, 0]])
        self.assertEqual(scene._slots(), [(NEEDLE_ACTION, 1), (64, 1)] + [(0, 0)] * 5)
        self.assertEqual(scene._smart_use(), 64)  # the needle is no use outside a bubble
        seventh = self.scene([[0, 0]] * 6 + [[41, 1]])
        self.assertEqual(seventh._smart_use(), 0)  # Ctrl looks at slots 0..5 only

    def test_trapped_ctrl_uses_needle(self) -> None:
        self.assertEqual(self.scene([[41, 1], [NEEDLE_ACTION, 1]], ACTOR_TRAPPED)._smart_use(), NEEDLE_ACTION)
        self.assertEqual(self.scene([[41, 1]], ACTOR_TRAPPED)._smart_use(), 0)


class TreasureScoreTest(unittest.TestCase):
    @staticmethod
    def scene(actors: list[dict]) -> tuple[BattleScene, list[tuple[str, int, int, int]]]:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id, scene.treasure_scored, scene.seats, scene.rule = 1, False, {}, "treasure"
        scene.state = {"actors": actors}
        drawn: list[tuple[str, int, int, int]] = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, frame, x, y))  # type: ignore[method-assign]
        return scene, drawn

    def test_digits_and_leader(self) -> None:
        actors = [{"id": 1, "team": 1, "state": 0, "carried": [0, 0, 0, 0]},
                  {"id": 2, "team": 2, "state": 0, "carried": [0, 0, 0, 0]}]
        scene, drawn = self.scene(actors)
        scene._draw_treasure(None)  # type: ignore[arg-type]
        # No score yet: both badges plain, both values 0.
        self.assertEqual(drawn, [(TREASURE_BADGE, 0, 624, 111), (TREASURE_DIGITS[1], 0, 628, 121),
                                 (TREASURE_BADGE, 0, 624, 162), (TREASURE_DIGITS[0], 0, 628, 172)])
        actors[1]["carried"] = [2, 1, 3, 0]  # 2 + 2 + 9 = 13
        drawn.clear()
        scene._draw_treasure(None)  # type: ignore[arg-type]
        self.assertEqual(drawn, [(TREASURE_BADGE, 0, 624, 111), (TREASURE_DIGITS[1], 0, 628, 121),
                                 (TREASURE_BADGE, 1, 624, 162), (TREASURE_DIGITS[0], 1, 621, 172),
                                 (TREASURE_DIGITS[0], 3, 635, 172)])

    def test_rows_follow_room_seats(self) -> None:
        actors = [{"id": 1, "team": 1, "state": 0, "carried": [1, 0, 0, 0]},
                  {"id": 2, "team": 2, "state": 0, "carried": [0, 0, 0, 0]}]
        scene, drawn = self.scene(actors)
        scene.seats = {1: 3, 2: 0}
        scene._draw_treasure(None)  # type: ignore[arg-type]
        self.assertEqual([(path, y) for path, _frame, _x, y in drawn if path == TREASURE_BADGE], [(TREASURE_BADGE, 111 + 51 * 3), (TREASURE_BADGE, 111)])

    def test_wrestle_counts_kills(self) -> None:
        actors = [{"id": 1, "team": 1, "state": ACTOR_ELIMINATED}, {"id": 2, "team": 2, "state": 0}]
        scene, drawn = self.scene(actors)
        scene.rule = "wrestle"
        scene.state["kills"] = {}
        scene._draw_treasure(None)  # type: ignore[arg-type]
        # A death without a kill leaves every badge blue.
        self.assertEqual([frame for path, frame, _x, _y in drawn if path == TREASURE_BADGE], [0, 0])
        drawn.clear()
        scene.state["kills"] = {"2": 5}
        scene._draw_treasure(None)  # type: ignore[arg-type]
        self.assertEqual(drawn, [(TREASURE_BADGE, 0, 624, 111), (TREASURE_DIGITS[1], 0, 628, 121),
                                 (TREASURE_BADGE, 1, 624, 162), (TREASURE_DIGITS[0], 5, 628, 172)])

    def test_respawn_clears_death_timers(self) -> None:
        scene, _drawn = self.scene([{"id": 2, "team": 2, "state": ACTOR_ELIMINATED, "carried": [0, 0, 0, 0]}])
        scene.eliminated_at, scene.trapped_at, scene.walk_started = {2: 5.0}, {2: 1.0}, {2: 7}
        scene._apply({"kind": EVENT_ACTOR_RESPAWNED, "player": 2, "cell": {"row": 1, "col": 1}})
        self.assertEqual((scene.eliminated_at, scene.trapped_at, scene.walk_started), ({}, {}, {}))


class BunHouseTest(unittest.TestCase):
    def test_houses_replace_map_elements_and_draw_their_buns(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        cells = 15 * 13
        top = [0] * cells
        top[1 * 15 + 4], top[1 * 15 + 5], top[2 * 15 + 4] = 8009, -8009, -8009  # map 806 draws its own house
        top[0] = 3004
        scene.map = mapfile.MapFile(3, 3, 15, 13, [top, [0] * cells, [0] * cells])
        scene.now_ms = 250.0
        scene.state = {"bun": {"first": 2, "houses": [{"row": 1, "col": 4, "team": 2, "own": 2, "captured": 1},
                                                      {"row": 1, "col": 8, "team": 1, "own": 0, "captured": 0}]}}
        scene._place_bun_houses()
        self.assertEqual((top[1 * 15 + 4], top[1 * 15 + 5], top[2 * 15 + 4], top[0]), (0, 0, 0, 3004))
        drawn: list[tuple[str, int, int, int]] = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, frame, x, y))  # type: ignore[method-assign]
        scene._draw_bun_houses(None, 2)  # type: ignore[arg-type]
        self.assertEqual(drawn, [])
        scene._draw_bun_houses(None, 1)  # type: ignore[arg-type]
        # Field origin (10, 27): house 1 pixel right of element 8009's offset
        # (1, 20); own buns in the roof window 8 apart, captured ones 10 lower.
        self.assertEqual(drawn, [(BUN_HOUSES[0], 2, 170, 47), (BUN_ICONS[0], 0, 213, 50), (BUN_ICONS[0], 0, 221, 50),
                                 (BUN_ICONS[1], 0, 213, 60), (BUN_HOUSES[1], 2, 330, 47)])


class StatueTest(unittest.TestCase):
    def test_statue_layers_and_trigger(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        cells = 15 * 13
        top = [0] * cells
        top[11 * 15 + 3] = 12008  # a map that draws its own statue
        scene.map = mapfile.MapFile(3, 6, 15, 13, [top, [0] * cells, [0] * cells])
        scene.now_ms = 5000.0
        scene.state = {"actors": [{"id": 7, "role": 3}], "sculpture": {"statues": [
            {"row": 11, "col": 3, "team": 1, "first": True, "progress": 2, "slots": [12, 12], "completed_by": 0},
            {"row": 11, "col": 11, "team": 2, "first": False, "progress": 4, "slots": [12, 12, 12, 12], "completed_by": 7}]}}
        scene._place_statues()
        self.assertEqual(top[11 * 15 + 3], 0)
        scene.statue_changed = {(11, 3): (2, 4500.0)}
        scene.sprites = type("Sprites", (), {"get": staticmethod(lambda path: [(None, 0, 0)] * 3)})()
        drawn: list[tuple[str, int, int, int]] = []
        scene._draw_frames = lambda _screen, path, frame, x, y, additive=False: drawn.append((path, frame, x, y))  # type: ignore[method-assign]
        scene._draw_statues(None, 11)  # type: ignore[arg-type]
        # Field origin (10, 27); base anchor (11, 47), stage (11, 104), hero (12, 150).
        self.assertEqual(drawn, [
            ("object/misc/misc221_stand.img", 0, 10 + 120 - 11, 27 + 440 - 47),
            ("object/misc/misc224_trigger.img", 5, 10 + 120 - 11, 27 + 440 - 104),
            ("object/misc/misc222_stand.img", 0, 10 + 440 - 11, 27 + 440 - 47),
            ("object/misc/misc229_stand.img", 0, 10 + 440 - 11, 27 + 440 - 104),
            ("object/misc/misc233_stand.img", 0, 10 + 440 - 12, 27 + 440 - 150)])


class FlightTest(unittest.TestCase):
    def test_sideways_flight_arcs_and_lands(self) -> None:
        # A bubble dropped in from column 14 of row 5 onto column 11.
        flight = {"from": {"row": 5, "col": 14}, "dir": 2, "cells": 3, "start": 5000, "until": 5300}
        self.assertEqual(flight_position(flight, 5000, 600, 520), (560, 200))
        self.assertEqual(flight_position(flight, 5150, 600, 520), (500, 170))  # 120 px flight, 30 px high mid-way
        self.assertEqual(flight_position(flight, 5300, 600, 520), (440, 200))

    def test_kick_wraps_round_the_field(self) -> None:
        # Tutorial3.qbv's first kick: down from row 10 to row 5, eight cells.
        flight = {"from": {"row": 10, "col": 10}, "dir": 3, "cells": 8, "start": 0, "until": 800}
        self.assertEqual(flight_position(flight, 400, 600, 520), (400, 40))  # past the bottom edge, back at the top
        self.assertEqual(flight_position(flight, 800, 600, 520), (400, 200))


    def test_machine_throw_interpolates_diagonal_and_wraps(self) -> None:
        flight = {"from": {"row": 5, "col": 14}, "to": {"row": 6, "col": 5},
                  "dir": 0, "cells": 0, "start": 0, "until": 800}
        self.assertEqual(flight_position(flight, 0, 600, 520), (560, 200))
        # 240 px horizontally, 40 px down; at half flight the arc is 60 high.
        self.assertEqual(flight_position(flight, 400, 600, 520), (80, 160))
        self.assertEqual(flight_position(flight, 800, 600, 520), (200, 240))

    def test_edge_crossing_uses_the_bubble_centre(self) -> None:
        right = {"from": {"row": 5, "col": 14}, "dir": 0, "cells": 8, "start": 0, "until": 800}
        # At 40 ms its centre is 596, still at the right edge; at 60 ms it
        # is 604 and appears at the left edge, not 20 px later.
        self.assertEqual(flight_position(right, 40, 600, 520), (576, 185))
        self.assertEqual(flight_position(right, 60, 600, 520), (-16, 178))
        down = {"from": {"row": 12, "col": 5}, "dir": 3, "cells": 8, "start": 0, "until": 800}
        self.assertEqual(flight_position(down, 40, 600, 520), (200, 496))
        self.assertEqual(flight_position(down, 60, 600, 520), (200, -16))

    def test_normal_and_power_bubbles_use_native_anchor(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.streak_badge = None
        scene.map = mapfile.MapFile(3, 2, 15, 13, [[0] * 195 for _ in range(3)])
        scene.dying = {}
        scene.state = {"clock": 0, "actors": [], "pickups": [], "flames": [], "bombs": [
            {"id": 1, "row": 1, "col": 2}, {"id": 2, "row": 2, "col": 3, "style": 2}]}
        drawn = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, x, y))
        scene._draw_element = lambda *_args: None
        scene.draw(pygame.Surface((800, 600)))
        # 0x005DF590 places both at cell centres; 0x005DF6FB subtracts
        # anchor (0,15), as confirmed against the calibrated rule-2 video.
        self.assertEqual(drawn[:2], [("object/bomb/bomb1_stand.img", 90, 52),
                                     ("object/bomb/bomb2_stand.img", 130, 92)])


class MachineDrawingTest(unittest.TestCase):
    def test_machine_durability_uses_team_frames_without_badges(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id, scene.seats = 1, {1: 0, 2: 3}
        scene.state = {"actors": [{"id": 1, "team": 1, "durability": 3}, {"id": 2, "team": 2, "durability": 1}]}
        drawn = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, frame, x, y))
        scene._draw_machine_durability(None)
        self.assertEqual(drawn, [("object/misc/misc460_stand.img", 3, 628, 121),
                                ("object/misc/misc461_stand.img", 1, 628, 274)])

    def test_ready_machine_draws_native_gate_and_local_arrow(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id = 1
        scene.state = {"clock": 48000, "actors": [{"id": 1, "team": 1}], "machine": {"gates": [
            {"row": 3, "col": 6, "first": True, "team": 1, "ready": True, "fired": 0}]}}
        drawn = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, x, y))
        scene._draw_machine_gates(None, 3)
        self.assertEqual(drawn, [("object/misc/misc401_stand.img", 210, 67),
                                ("object/misc/misc400_stand.img", 230, 67)])


class BoxDrawingTest(unittest.TestCase):
    def test_box_uses_moving_pixel_position(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.state = {"clock": 1000, "box": {"objects": [
            {"id": 67, "element": 14112, "row": 4, "col": 3, "x": 200, "y": 180,
             "moving": True, "destroy_at": 0, "born": 0, "hidden": False}]}}
        scene.elements = {14112: mapfile.ElementInfo(offset=(3, 15))}
        drawn = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, x, y))
        scene._draw_boxes(None, 4)
        self.assertEqual(drawn, [("object/mapElem/box/elem112_stand.img", 187, 172)])

    def test_box_health_is_above_actor_and_other_player_has_background(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id = 1
        drawn = []
        scene._draw_frames = lambda _screen, path, frame, x, y: drawn.append((path, x, y))
        actor = {"id": 1, "state": 0, "durability": 4, "x": 100, "y": 200}
        scene._draw_box_health(None, actor)
        self.assertEqual(drawn, [("object/misc/misc194_stand.img", 90, 158)])
        drawn.clear()
        actor.update(id=2, durability=3)
        scene._draw_box_health(None, actor)
        self.assertEqual(drawn, [("object/misc/misc196_stand.img", 89, 157),
                                ("object/misc/misc193_stand.img", 90, 158)])


class EffectTest(unittest.TestCase):
    def test_effect_frames_wrap_at_the_last_key(self) -> None:
        # default_MapElement.eff: 4 frames of 100 ms, keys 0..1000.
        self.assertEqual([effect_frame(t, 1000, 100, 4) for t in range(0, 1200, 100)], [0, 1, 2, 3, 0, 1, 2, 3, 0, 1, 0, 1])
        # flash2.eff: 15 frames of 100 ms, keys 0..1500.
        self.assertEqual([effect_frame(t, 1500, 100, 15) for t in (0, 1499, 1500, 2950)], [0, 14, 0, 14])

    def test_protection_flash_is_drawn_only_while_protected(self) -> None:
        scene = BattleScene.__new__(BattleScene)
        scene.state = {"clock": 1000}
        scene.flash_ms = {2: 1650}
        scene.sprites = type("Sprites", (), {"get": staticmethod(lambda path: [(None, 0, 0)] * 15 if path == FLASH_IMAGE else None)})()
        drawn: list[tuple[str, int, int, int]] = []
        scene._draw_frames = lambda _screen, path, frame, x, y, additive=False: drawn.append((path, frame, x, y))  # type: ignore[method-assign]
        actor = {"id": 2, "x": 100, "y": 60, "protected_until": 1500}
        scene._draw_flash(None, actor, 110, 87)  # type: ignore[arg-type]
        # Effect point = sprite point = position + (10, 30) -> field + (2, 9).
        self.assertEqual(drawn, [(FLASH_IMAGE, 1, 112, 96)])
        drawn.clear()
        actor["protected_until"] = 1000
        scene._draw_flash(None, actor, 110, 87)  # type: ignore[arg-type]
        self.assertEqual(drawn, [])

    def test_item_anchors(self) -> None:
        # Cell (1, 2): field origin (10, 27); anchor (0, 15) by default.
        self.assertEqual(BattleScene._item_at(6, 1, 2), (90, 52))
        self.assertEqual(BattleScene._item_at(42, 1, 2), (90, 52))  # placed banana
        self.assertEqual(BattleScene._item_at(201, 1, 2), (85, 47))
        self.assertEqual(BattleScene._item_at(203, 1, 2), (90, 67))


class PlayerTitleTest(unittest.TestCase):
    def test_titles(self) -> None:
        self.assertEqual(player_title(0, 1), "QQ堂平民")
        self.assertEqual(player_title(9999, 1), "QQ堂平民")
        self.assertEqual(player_title(10000, 1), "糖果爱好者")
        self.assertEqual(player_title(1150000, 0), "刨冰骑兵")  # Client.exe's table, not levelCFG's
        self.assertEqual(player_title(10800000, 1), "太阳国王")
        self.assertEqual(player_title(10800000, 0), "太阳女王")
        self.assertEqual(player_title(144100000, 1), "???")


class PracticeTest(unittest.TestCase):
    def test_players(self) -> None:
        players = practice_players(1, 2)
        self.assertEqual([(p["id"], p["team"], p.get("ai", False)) for p in players], [(1, 1, False), (2, 2, True), (3, 3, True)])


if __name__ == "__main__":
    unittest.main()

class TankTest(unittest.TestCase):
    def test_base_damage_stages_and_team_numbers(self):
        scene = BattleScene.__new__(BattleScene)
        scene.state = {"clock": 5000, "tank": {"bases": [
            {"team": 2, "row": 9, "col": 11, "hp": 70},
            {"team": 1, "row": 1, "col": 1, "hp": 0},
        ]}}
        draws, calls = [], []
        scene.shown_tank_hp = {}
        scene._draw_frames = lambda *args: draws.append(args)
        scene._script = lambda *args: calls.append(args)
        scene._draw_tank_bases(None, 9)
        scene._draw_tank_bases(None, 1)
        self.assertEqual([row[1] for row in draws], ["object/misc/misc624_stand.img", "object/misc/misc630_stand.img"])
        scene._update_tank_bases()
        self.assertEqual(calls, [("ui_UpdateTankBaseLife", 2, 70), ("ui_UpdateTankBaseLife", 1, 0)])

    def test_binary_element_catalog_supplements_script(self):
        binary = struct.pack("<II", 2, 2)
        binary += struct.pack("<Ihhhh iII I", 19033, 1, 1, 3, 12, 1, 0, 0, 0)
        binary += struct.pack("<Ihhhh iII I", 3004, 1, 1, 9, 9, 99, 0, 0, 0)
        script = b"class QQTMapElem3004:\n    offset=(2,13)\n    LifeTime=1\n"
        elements = mapfile.load_elements(FakeResources({"object/mapElem/mapElem.prop": binary, "object/mapElem/mapElem.py": script}))
        self.assertEqual((elements[19033].offset, elements[19033].life_time), ((3, 12), 1))
        self.assertEqual((elements[3004].offset, elements[3004].life_time), ((2, 13), 1))
        with self.assertRaises(ValueError):
            mapfile.parse_element_properties(binary[:-1])
