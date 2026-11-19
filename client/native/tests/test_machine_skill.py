"""Rule 7's charged skills: input, combo recognition, sending and poses."""

from __future__ import annotations

import json
import pathlib
import sys
import types
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))

from qqtclient.battle.machine_skill import (COMBOS, KEY_DOWN, KEY_LEFT, KEY_RIGHT, SKILL_CHARGE, SkillInput,  # noqa: E402
                                           SkillPose, held_move_keys, pose_image, pose_sound)
from qqtclient.battle.scene import BattleScene  # noqa: E402
from qqtclient.battle.sidecar import MOVE_DOWN, MOVE_LEFT, MOVE_RIGHT, MOVE_UP  # noqa: E402
from qqtclient.keylayout import DEFAULT_LAYOUT, BattleKeys  # noqa: E402


class Player:
    """Drives SkillInput one 20 ms frame at a time."""

    def __init__(self, facing: int = MOVE_RIGHT, first_team: bool = True):
        self.input = SkillInput()
        self.now = 3000
        self.bubble = False
        self.keys: set[int] = set()
        self.facing, self.first_team = facing, first_team
        self.sent: list[tuple[int, int]] = []
        self.frame()  # bubble key up: the charge clock starts here

    def frame(self, frames: int = 1) -> None:
        for _ in range(frames):
            self.now += 20
            self.input.poll(self.now, self.bubble, self.keys)
            skill = self.input.send(self.now, self.facing, self.first_team)
            if skill is not None:
                self.sent.append((self.now, skill))

    def until(self, held_ms: int) -> None:
        while self.now - self.input.charge_start < held_ms:
            self.frame()

    def tap(self, key: int) -> None:
        self.keys.add(key)
        self.frame(2)
        self.keys.discard(key)
        self.frame(2)


class ComboTableTest(unittest.TestCase):
    def test_table_matches_0x007A23D4(self) -> None:
        # Raw shorts 4,1,4,1 / 4,2,4,2 / 2,1 / 1,2 / 1,4,2 / 2,4,1 / 2,2 / 1,1
        # with key 1 left, 2 right, 3 up, 4 down; lengths 4,4,2,2,3,3,2,2.
        self.assertEqual([tuple(combo) for combo in COMBOS],
                         [(4, 1, 4, 1), (4, 2, 4, 2), (2, 1), (1, 2), (1, 4, 2), (2, 4, 1), (2, 2), (1, 1)])

    def test_every_combo_after_arming(self) -> None:
        for number, combo in enumerate(COMBOS):
            player = Player()
            player.bubble = True
            player.frame()
            player.until(2040)
            for key in combo:
                player.tap(key)
            self.assertEqual(player.sent[-1][1], number, combo)
            self.assertEqual(player.sent[0][1], SKILL_CHARGE)


class NativeRecordTest(unittest.TestCase):
    def test_matches_unicorn_run_of_the_v848_input_poll(self) -> None:
        """tools/probe_machine_skill.py: the real 0x005A4C52 / 0x005CAF0B /
        0x005A53DE on scripted key states, frame by frame."""
        fixture = pathlib.Path(__file__).resolve().parent / "fixtures" / "machine_skill_v848.jsonl"
        rows = [json.loads(line) for line in fixture.read_text().splitlines()]
        skill_input, case = SkillInput(), None
        cases = set()
        for row in rows:
            if row["case"] != case:
                case = row["case"]
                cases.add(case)
                skill_input.notice()
            skill_input.poll(row["time"], row["bubble"], set(row["keys"]))
            value = skill_input.value(row["time"])
            self.assertEqual((value, skill_input.recognizer.skill), (row["value"], row["skill"]), row)
        self.assertEqual(len(cases), 14)
        self.assertEqual({row["skill"] for row in rows if row["case"].startswith("combo")} - {-1}, set(range(8)))


class SkillInputTest(unittest.TestCase):
    def test_charge_notice_once_then_nothing_before_arming(self) -> None:
        player = Player()
        player.bubble = True
        player.frame()
        player.until(1600)
        self.assertEqual([skill for _, skill in player.sent], [SKILL_CHARGE])
        sent_at = player.sent[0][0] - player.input.charge_start
        self.assertTrue(900 < sent_at <= 920, sent_at)
        # Keys before the 2000 ms mark are not recorded.
        player.tap(KEY_RIGHT)
        player.tap(KEY_LEFT)
        player.until(2100)
        self.assertEqual(len(player.sent), 1)
        self.assertEqual(player.input.recognizer.skill, -1)

    def test_slow_keys_restart_the_sequence(self) -> None:
        player = Player()
        player.bubble = True
        player.frame()
        player.until(2040)
        player.tap(KEY_RIGHT)
        player.frame(16)  # more than 300 ms
        player.tap(KEY_LEFT)
        self.assertEqual(player.input.recognizer.skill, -1)
        player.tap(KEY_LEFT)  # within 300 ms: L L
        self.assertEqual(player.sent[-1][1], 7)

    def test_too_late_and_released(self) -> None:
        player = Player()
        player.bubble = True
        player.frame()
        player.until(3000)
        player.frame()
        player.tap(KEY_RIGHT)
        player.tap(KEY_LEFT)
        self.assertEqual([skill for _, skill in player.sent], [SKILL_CHARGE])
        player.bubble = False
        player.frame()
        self.assertEqual(player.input.charge_start, player.now)

    def test_facing_the_own_half_resets_and_sends_nothing(self) -> None:
        for facing, first in ((MOVE_LEFT, True), (MOVE_RIGHT, False), (MOVE_UP, True), (MOVE_DOWN, False)):
            player = Player(facing, first)
            player.bubble = True
            player.frame()
            player.until(2040)
            player.tap(KEY_RIGHT)
            player.tap(KEY_RIGHT)
            self.assertEqual(player.sent, [], (facing, first))
        player = Player(MOVE_LEFT, False)
        player.bubble = True
        player.frame()
        player.until(2040)
        for key in COMBOS[1]:
            player.tap(key)
        self.assertEqual([skill for _, skill in player.sent], [SKILL_CHARGE, 1])

    def test_unchanged_value_is_sent_once_and_notice_resets(self) -> None:
        player = Player()
        player.bubble = True
        player.frame()
        player.until(2040)
        player.tap(KEY_LEFT)
        player.tap(KEY_RIGHT)
        player.frame(10)
        self.assertEqual([skill for _, skill in player.sent], [SKILL_CHARGE, 3])
        player.input.notice()  # the 0x115B came back
        self.assertEqual(player.input.recognizer.skill, -1)
        player.frame()
        # Re-arming needs the bubble key again.
        player.tap(KEY_LEFT)
        player.tap(KEY_RIGHT)
        self.assertEqual(len(player.sent), 2)

    def test_mapping_of_layout_moves(self) -> None:
        self.assertEqual(held_move_keys({MOVE_LEFT, MOVE_DOWN}), {KEY_LEFT, KEY_DOWN})


class PoseTest(unittest.TestCase):
    def test_images_and_clock(self) -> None:
        self.assertEqual([pose_image(pose) for pose in (0, 1, 2, 4, 6, 7, 8)],
                         [f"object/misc/misc{n}_stand.img" for n in (471, 471, 472, 473, 474, 475, 470)])
        pose = SkillPose.start(0, 5000, 6)
        self.assertEqual(pose.picture.frame, 0)
        clock = 5000
        frames = []
        while pose.advance(clock + 20, 20):
            clock += 20
            frames.append(pose.picture.frame)
        # 100 ms + 1.5 x 20 per frame: frame 1 after 140 ms, 0 again (wrap)
        # after 1140 ms; gone after 1200.
        self.assertEqual(frames[5:8], [0, 1, 1])
        self.assertEqual(frames[-1], 0)
        self.assertEqual(clock - 5000, 1200)
        charge = SkillPose.start(SKILL_CHARGE, 0, 13)
        self.assertEqual(charge.offset(MOVE_RIGHT), (-15, -70))
        self.assertEqual(charge.offset(MOVE_LEFT), (-65, -70))
        self.assertIsNone(charge.offset(MOVE_UP))
        self.assertEqual(pose.offset(MOVE_UP), (-20, -85))


class SceneInputTest(unittest.TestCase):
    def scene(self) -> BattleScene:
        scene = BattleScene.__new__(BattleScene)
        scene.keys = BattleKeys.from_layout(DEFAULT_LAYOUT)
        scene.held = []
        scene.bomb_pressed = False
        scene.skill_input = SkillInput()
        scene.skill_poses = {}
        scene.runtime = None
        scene.over = False
        scene.local_id = 1
        scene.state = {"clock": 6000, "actors": [{"id": 1, "team": 1, "state": 0, "facing": MOVE_RIGHT, "x": 100, "y": 100}],
                       "machine": {"first_team": 1}}
        return scene

    def key(self, scene: BattleScene, kind: int, key: int) -> None:
        scene.handle(types.SimpleNamespace(type=kind, key=key))

    def test_directions_with_the_bubble_key_do_not_move(self) -> None:
        scene = self.scene()
        self.key(scene, pygame.KEYDOWN, pygame.K_SPACE)
        self.key(scene, pygame.KEYDOWN, pygame.K_RIGHT)
        self.assertEqual(scene.held, [])
        self.assertIsNone(scene._poll_skill())
        self.assertEqual(scene.held, [])
        # Released bubble key: the still-held direction starts moving.
        self.key(scene, pygame.KEYUP, pygame.K_SPACE)
        scene._poll_skill()
        self.assertEqual(scene.held, [MOVE_RIGHT])
        self.key(scene, pygame.KEYUP, pygame.K_RIGHT)
        self.assertEqual(scene.held, [])
        # A direction held before the bubble key keeps moving.
        self.key(scene, pygame.KEYDOWN, pygame.K_LEFT)
        self.key(scene, pygame.KEYDOWN, pygame.K_SPACE)
        self.assertEqual(scene.held, [MOVE_LEFT])

    def test_other_rules_are_unchanged(self) -> None:
        scene = self.scene()
        scene.skill_input = None
        self.key(scene, pygame.KEYDOWN, pygame.K_SPACE)
        self.key(scene, pygame.KEYDOWN, pygame.K_RIGHT)
        self.assertEqual(scene.held, [MOVE_RIGHT])
        self.assertIsNone(scene._poll_skill())

    def test_notice_starts_pose_and_resets_local_input(self) -> None:
        scene = self.scene()
        scene.sprites = types.SimpleNamespace(get=lambda path: [None] * 6)
        scene.skill_input.recognizer.skill = 4
        scene.state["machine"]["skills"] = [{"player": 1, "type": 4, "pose": 4, "time": 6000}]
        scene._update_skill_poses({1: (100, 100)}, 20)
        self.assertEqual(scene.skill_input.recognizer.skill, -1)
        self.assertEqual(scene.skill_poses[1].pose, 4)
        # A refused skill (pose -1) shows nothing.
        scene.state["machine"]["skills"] = [{"player": 1, "type": 9, "pose": -1, "time": 6020}]
        scene.skill_poses = {}
        scene._update_skill_poses({1: (100, 100)}, 20)
        self.assertEqual(scene.skill_poses, {})

    def test_sounds_follow_our_own_poses(self) -> None:
        # 0x005FF2B8: x26_01 for the charge, x27_01 for a throw, kick or a
        # dodge that moved; only for our player (0x00534990).
        scene = self.scene()
        played = []
        scene.runtime = types.SimpleNamespace(namespace={"PlaySound": lambda name, loops: played.append((name, loops))})
        scene.sprites = types.SimpleNamespace(get=lambda path: [None] * 6)
        notices = [(1, 8), (1, 0), (1, 7), (1, -1), (2, 4), (2, 8)]
        for player, pose in notices:
            scene.state["machine"]["skills"] = [{"player": player, "type": max(pose, 9), "pose": pose, "time": 6000}]
            scene._update_skill_poses({}, 20)
        self.assertEqual(played, [("x26_01.wav", 1), ("x27_01.wav", 1), ("x27_01.wav", 1)])
        self.assertEqual((pose_sound(6), pose_sound(9), pose_sound(-1)), ("x27_01.wav", None, None))
        scene.runtime = None
        scene._update_skill_poses({}, 20)  # no UI: silent, no error


if __name__ == "__main__":
    unittest.main()
