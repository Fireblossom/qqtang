import json
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.avatar import AvatarCache
from qqtclient.battle.result import ResultDisplay
from qqtclient.battle.result_portrait import ResultPortrait


class PortraitResources:
    def __init__(self, configs):
        self.configs = configs

    def read(self, path):
        if path == "colorlist.dat":
            return None
        return self.configs.get(path)


class ResultPortraitTest(unittest.TestCase):
    @staticmethod
    def native():
        path = pathlib.Path(__file__).parent / "fixtures/result_portrait_v848.jsonl"
        return list(map(json.loads, path.read_text().splitlines()))

    def test_native_pose_guards_positions_and_part_clock(self):
        models = {}
        checked = 0
        for row in self.native():
            if row["kind"] != "pose":
                continue
            key = row["role"], row["outcome"]
            portrait = models.setdefault(key, ResultPortrait(row["role"], row["outcome"], {"controlled": row["frame_count"]}))
            portrait.advance(row["dt"])
            part = portrait.parts["controlled"]
            with self.subTest(role=row["role"], outcome=row["outcome"], elapsed=row["elapsed"]):
                self.assertEqual({"stand": 1, "lose": 8, "win": 10}[portrait.action], row["action"])
                self.assertEqual(list(portrait.position), row["position"])
                self.assertEqual(list(portrait.shadow_position), row["shadow_position"])
                self.assertEqual((part.frame, part.remainder), (row["frame"], row["remainder"]))
            checked += 1
        self.assertEqual(checked, 144)

    def test_action_parts_use_stand_ids_and_native_filename_selection(self):
        rows = self.native()
        resources = PortraitResources({f"object/player/player{row['role']}.ini": row["ini"].encode("gbk")
                                       for row in rows if row["kind"] == "config"})
        avatars = AvatarCache(resources)
        for row in rows:
            if row["kind"] != "pose" or row["elapsed"] != 0:
                continue
            action = {1: "stand", 8: "lose", 10: "win"}[row["action"]]
            native = {part["path"].replace("\\", "/").split("/")[1]: part["base"] for part in row["parts"]}
            with self.subTest(role=row["role"], action=action):
                self.assertEqual(avatars.parts(row["role"], action), native)

    def test_clock_runs_during_the_result_wait(self):
        display = ResultDisplay(4, [], 1, {})
        display.portrait = ResultPortrait(1, 2, {"cloth": 3})
        display.advance(200)
        self.assertEqual(display.phase, 0)
        self.assertEqual(display.portrait.frames(), (("cloth", 1),))
