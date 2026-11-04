"""Result movement and drawing compared with the executed native controller."""

import json
import pathlib
import struct
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.result import ResultDisplay, experience_percent, experience_level, result_points
from qqtclient.battle.scene import ResultRow
from qqtclient.battle.online import MatchContext, MatchMember, OnlineBattle, RESULT_SHOWN_MS
from qqtclient.net import match


class ResultsTest(unittest.TestCase):
    @staticmethod
    def fixture(name):
        return list(map(json.loads, (pathlib.Path(__file__).parent / "fixtures" / name).read_text().splitlines()))

    @staticmethod
    def native():
        path = pathlib.Path(__file__).parent / "fixtures/results_v848.jsonl"
        return list(map(json.loads, path.read_text().splitlines()))

    @staticmethod
    def model(rule, outcome):
        return ResultDisplay(rule, [ResultRow(1, outcome, 2, 3, 100, 0, 0, (52, 63, 2000, 17), 7)], 1, {1: "糖五"})

    def test_matches_native_wait_fall_snap_and_fade_at_two_cadences(self):
        models = {}
        for row in self.native():
            if row["kind"] != "geometry":
                continue
            key = (row["rule"], row["cadence"])
            model = models.setdefault(key, self.model(row["rule"], row["outcome"]))
            while model.elapsed < row["elapsed"]:
                model.advance(row["cadence"])
            with self.subTest(rule=row["rule"], elapsed=row["elapsed"], cadence=row["cadence"]):
                self.assertEqual((model.phase, model.wait, model.alpha), (row["phase"], row["wait"], row["alpha"]))
                self.assertEqual(list(model.table_at), row["position"])
                self.assertEqual(model.plan()[0][0].number, row["table"])
                self.assertEqual(12000 - model.elapsed, row["lifetime"])

    def test_matches_native_labels_paired_scores_digit_positions_and_outcomes(self):
        for row in self.native():
            if row["kind"] != "draw":
                continue
            model = self.model(row["rule"], row["outcome"])
            for _ in range(row["elapsed"] // row["cadence"]):
                model.advance(row["cadence"])
            images, texts = model.plan()
            with self.subTest(rule=row["rule"], outcome=row["outcome"]):
                self.assertEqual([dict(number=i.number, frame=i.frame, position=[i.x, i.y], alpha=i.alpha) for i in images], row["images"])
                self.assertEqual([[t.x, t.y, t.text, t.alpha << 24 | 0x05576B, 12] for t in texts], row["texts"])

    def test_game_over_keeps_each_score_pair_and_the_fourth_ordinal(self):
        battle = OnlineBattle.__new__(OnlineBattle)
        battle.local_id, battle.next_stage, battle.elapsed_ms, battle.finish_at = 1, None, 123, None
        battle.context = MatchContext(False, {1: MatchMember(1000005, "糖五", 10000)}, lambda: None)
        captured = []
        battle.scene = types.SimpleNamespace(show_results=lambda rows: captured.extend(rows), map=types.SimpleNamespace(rule=1))
        body = struct.pack(">IBHBIIB", 1000, 1, 1, match.RESULT_WIN, 0, 50, 4)
        body += struct.pack(">8I", 2, 3, 100, 7, 52, 63, 2000, 17) + b"\0"
        payload = struct.pack(">HII", 1, 1, len(body) + 4) + struct.pack("<I", match.SCHEMA_GAME_OVER) + body
        battle.on_game_event(types.SimpleNamespace(payload=payload))
        self.assertEqual(captured, [ResultRow(1, match.RESULT_WIN, 2, 3, 100, 50, 12132, (52, 63, 2000, 17), 7, 10000)])
        self.assertTrue(battle.scene.over)
        self.assertEqual(battle.finish_at, 123 + RESULT_SHOWN_MS)

    def test_native_game_over_projection_percentages_totals_and_boundary_levels(self):
        rows = self.fixture("game_over_v848.jsonl")
        thresholds = rows[0]["values"]
        self.assertEqual(len(thresholds), 181)
        checked = 0
        for row in rows[1:]:
            native = row["display"]
            points = result_points(row["rule"], row["points"], row["scores"])
            with self.subTest(points=row["points"], gain=row["gain"], rule=row["rule"], section=row["section_mode"]):
                self.assertEqual(points - row["points"], native["score_total"])
                self.assertEqual(experience_level(points, thresholds), native["level"])
                if row["section_mode"] != 1:
                    self.assertEqual([experience_percent(row["points"], thresholds),
                                      experience_percent(row["points"] + row["gain"], thresholds)], native["progress"])
                    result = ResultRow(1, 2, 2, 3, 100, row["gain"], points, tuple(row["scores"]), points_before=row["points"])
                    model = ResultDisplay(row["rule"], [result], 1, {}, thresholds)
                    self.assertEqual(model.progress, native["progress"])
                    model.phase = 3
                    icons = model.level_icons()
                    self.assertEqual(icons[0][2], thresholds[native["level"]] - 1)
                self.assertEqual(native["remark"], row["remark"])
            checked += 1
        self.assertEqual(checked, 36)

    def test_native_progress_updates_and_raw_texture_rectangles(self):
        for row in self.fixture("result_progress_v848.jsonl"):
            model = ResultDisplay(12 if row["base"] == 130 else 4, [], 1, {},
                                  progress=(row["start"], row["target"]))
            model.phase = 3
            for _ in range(row["elapsed"] // row["cadence"]):
                model.advance(row["cadence"])
            with self.subTest(base=row["base"], start=row["start"], target=row["target"], cadence=row["cadence"], elapsed=row["elapsed"]):
                self.assertEqual(model.progress[0], row["current"])
                self.assertEqual(model.alpha, row["alpha"])
                bars = [image for image in model.plan()[0] if image.clip_width is not None]
                self.assertEqual([image.number for image in bars], [quad["texture"] for quad in row["quads"]])
                for image, quad in zip(bars, row["quads"]):
                    vertices = [quad["values"][offset:offset + 5] for offset in range(0, 20, 5)]
                    self.assertEqual(image.clip_width, vertices[2][3])
                    self.assertEqual(image.x, vertices[0][0] + 400)
                    self.assertEqual(image.y, 300 - vertices[1][1])
                    self.assertEqual(image.alpha, 255)  # raw bars do not share the fade

    def test_honor_and_identity_override_match_native_draw_order_and_positions(self):
        for row in self.fixture("result_honors_v848.jsonl"):
            model = self.model(row["rule"], row["outcome"])
            model.rows[0].remark = row["remark"]
            model.rows[0].identity = row["profile_flags"]
            for _ in range(row["elapsed"] // row["cadence"]):
                model.advance(row["cadence"])
            with self.subTest(remark=row["remark"], identity=row["profile_flags"]):
                self.assertEqual([dict(number=i.number, frame=i.frame, position=[i.x, i.y], alpha=i.alpha)
                                  for i in model.plan()[0]], row["images"])

    def test_wire_remark_and_room_identity_reach_the_display_row(self):
        battle = OnlineBattle.__new__(OnlineBattle)
        battle.local_id, battle.next_stage, battle.elapsed_ms, battle.finish_at = 1, None, 123, None
        battle.context = MatchContext(False, {1: MatchMember(1000005, "糖五", 950, identity=0x2000)}, lambda: None)
        captured = []
        battle.scene = types.SimpleNamespace(show_results=lambda rows: captured.extend(rows), map=types.SimpleNamespace(rule=5))
        body = struct.pack(">IBHBIIB", 1000, 1, 1, match.RESULT_WIN, 101, 100, 4)
        body += struct.pack(">8I", 2, 3, 100, 7, 10, 20, 30, 40) + b"\0"
        payload = struct.pack(">HII", 1, 1, len(body) + 4) + struct.pack("<I", match.SCHEMA_GAME_OVER) + body
        battle.on_game_event(types.SimpleNamespace(payload=payload))
        self.assertEqual(captured, [ResultRow(1, 2, 2, 3, 100, 100, 1010, (10, 20, 30, 40), 7, 950, 101, 0x2000)])
