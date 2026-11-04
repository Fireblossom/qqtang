"""Result movement and drawing compared with the executed native controller."""

import json
import pathlib
import struct
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.result import ResultDisplay
from qqtclient.battle.scene import ResultRow
from qqtclient.battle.online import MatchContext, MatchMember, OnlineBattle, RESULT_SHOWN_MS
from qqtclient.net import match


class ResultsTest(unittest.TestCase):
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
        battle.scene = types.SimpleNamespace(show_results=lambda rows: captured.extend(rows))
        body = struct.pack(">IBHBIIB", 1000, 1, 1, match.RESULT_WIN, 0, 50, 4)
        body += struct.pack(">8I", 2, 3, 100, 7, 52, 63, 2000, 17) + b"\0"
        payload = struct.pack(">HII", 1, 1, len(body) + 4) + struct.pack("<I", match.SCHEMA_GAME_OVER) + body
        battle.on_game_event(types.SimpleNamespace(payload=payload))
        self.assertEqual(captured, [ResultRow(1, match.RESULT_WIN, 2, 3, 100, 50, 10050, (52, 63, 2000, 17), 7)])
        self.assertTrue(battle.scene.over)
        self.assertEqual(battle.finish_at, 123 + RESULT_SHOWN_MS)
