"""Battle map reading and drawing decisions; no client files or sidecar needed."""

from __future__ import annotations

import pathlib
import struct
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from qqtclient.battle import mapfile  # noqa: E402
from qqtclient.battle.practice import practice_players  # noqa: E402
from qqtclient.battle.scene import (ACTOR_TRAPPED, NEEDLE_ACTION, TREASURE_BADGE, TREASURE_DIGITS, BattleScene,  # noqa: E402
                                   Explosion, flame_image)
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
        scene.local_id, scene.selected_slot = 1, 0
        scene.state = {"actors": [{"id": 1, "state": state, "held": held}]}
        return scene

    def test_slots_and_switch(self) -> None:
        scene = self.scene([[41, 2], [64, 1], [0, 0], [44, 0]])
        self.assertEqual(scene._slots(), [(41, 2), (64, 1)] + [(0, 0)] * 5)
        self.assertEqual(scene._smart_use(), 41)
        scene._switch_slot()
        self.assertEqual((scene.selected_slot, scene._smart_use()), (1, 64))
        scene._switch_slot()  # wraps around past the empty slots
        self.assertEqual(scene.selected_slot, 0)

    def test_trapped_ctrl_uses_needle(self) -> None:
        self.assertEqual(self.scene([[41, 1], [NEEDLE_ACTION, 1]], ACTOR_TRAPPED)._smart_use(), NEEDLE_ACTION)
        self.assertEqual(self.scene([[41, 1]], ACTOR_TRAPPED)._smart_use(), 0)


class TreasureScoreTest(unittest.TestCase):
    @staticmethod
    def scene(actors: list[dict]) -> tuple[BattleScene, list[tuple[str, int, int, int]]]:
        scene = BattleScene.__new__(BattleScene)
        scene.local_id, scene.treasure_scored, scene.seats = 1, False, {}
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
        self.assertEqual(drawn, [(TREASURE_BADGE, 0, 636, 110), (TREASURE_DIGITS[1], 0, 640, 120),
                                 (TREASURE_BADGE, 0, 636, 161), (TREASURE_DIGITS[0], 0, 640, 171)])
        actors[1]["carried"] = [2, 1, 3, 0]  # 2 + 2 + 9 = 13
        drawn.clear()
        scene._draw_treasure(None)  # type: ignore[arg-type]
        self.assertEqual(drawn, [(TREASURE_BADGE, 0, 636, 110), (TREASURE_DIGITS[1], 0, 640, 120),
                                 (TREASURE_BADGE, 1, 636, 161), (TREASURE_DIGITS[0], 1, 633, 171),
                                 (TREASURE_DIGITS[0], 3, 647, 171)])

    def test_rows_follow_room_seats(self) -> None:
        actors = [{"id": 1, "team": 1, "state": 0, "carried": [1, 0, 0, 0]},
                  {"id": 2, "team": 2, "state": 0, "carried": [0, 0, 0, 0]}]
        scene, drawn = self.scene(actors)
        scene.seats = {1: 3, 2: 0}
        scene._draw_treasure(None)  # type: ignore[arg-type]
        self.assertEqual([(path, y) for path, _frame, _x, y in drawn if path == TREASURE_BADGE], [(TREASURE_BADGE, 110 + 51 * 3), (TREASURE_BADGE, 110)])

    def test_respawn_clears_death_timers(self) -> None:
        scene, _drawn = self.scene([{"id": 2, "team": 2, "state": ACTOR_ELIMINATED, "carried": [0, 0, 0, 0]}])
        scene.eliminated_at, scene.trapped_at, scene.walk_started = {2: 5.0}, {2: 1.0}, {2: 7}
        scene._apply({"kind": EVENT_ACTOR_RESPAWNED, "player": 2, "cell": {"row": 1, "col": 1}})
        self.assertEqual((scene.eliminated_at, scene.trapped_at, scene.walk_started), ({}, {}, {}))


class PracticeTest(unittest.TestCase):
    def test_players(self) -> None:
        players = practice_players(1, 2)
        self.assertEqual([(p["id"], p["team"], p.get("ai", False)) for p in players], [(1, 1, False), (2, 2, True), (3, 3, True)])


if __name__ == "__main__":
    unittest.main()
