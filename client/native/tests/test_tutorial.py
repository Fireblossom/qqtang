"""新手任务 flow: first-login gate, the end of the tutorial and the three
expert recordings (Client.exe 0x005A4181, 0x0048C960, 0x005A2C6D)."""

import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle import replay  # noqa: E402
from qqtclient.battle.tutorial import (ASK_RECORDS, ASK_RECORDS_TEXT, TutorialFlow, build_tutorial_natives,  # noqa: E402
                                       find_record, needs_first_tutorial)
from qqtclient.net.player import GameInfo  # noqa: E402


class FakeRuntime:
    def __init__(self):
        self.namespace = {}
        self.activity = None
        self.screens, self.boxes, self.posted, self.calls = [], [], [], []

    def post(self, function):
        self.posted.append(function)

    def run_posted(self):
        while self.posted:
            self.posted.pop(0)()

    def goto_screen(self, screen):
        self.screens.append(screen)

    def show_msgbox(self, text, caption, kind, target, handle):
        self.boxes.append((text, kind, handle))


class FakeReplay:
    started = []

    def __init__(self, runtime, renderer, resources, path, back):
        self.back, self.finished = back, False
        FakeReplay.started.append(path.name)

    def end(self, runtime):
        """ReplayBattle.finish(): back() runs while still the activity."""
        self.finished = True
        self.back()
        runtime.activity = None  # the main loop drops a finished activity


def profile(mode, wins, losses, draws):
    info = GameInfo(wins, losses, draws, *([0] * 10))
    return types.SimpleNamespace(section_mode=mode, game_info=info)


class TutorialTest(unittest.TestCase):
    def setUp(self):
        self.runtime = FakeRuntime()
        for name in ("NewPlayerPractice", "ui_msgBox", "ui_NotifyShowLevelIcon"):
            self.runtime.namespace[name] = lambda *args, name=name: self.runtime.calls.append((name,) + args)
        self.directory = tempfile.TemporaryDirectory()
        root = pathlib.Path(self.directory.name)
        for index in (1, 2, 3):
            (root / f"Tutorial{index}.qbv").write_bytes(b"")
        self.flow = TutorialFlow(self.runtime, None, types.SimpleNamespace(root=root))
        FakeReplay.started = []
        patcher = mock.patch.object(replay, "ReplayBattle", FakeReplay)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(self.directory.cleanup)

    def acknowledge_stage(self):
        text, _kind, handle = self.runtime.boxes.pop(0)
        self.assertIn("1501", text)
        handle(True)
        self.runtime.run_posted()

    def test_first_login_gate(self):
        self.assertTrue(needs_first_tutorial(profile(0, 0, 0, 0)))
        self.assertFalse(needs_first_tutorial(profile(0, 0, 0, 1)))
        self.assertFalse(needs_first_tutorial(profile(1, 0, 0, 0)))
        self.assertFalse(needs_first_tutorial(None))

    def test_first_tutorial_ends_with_new_player_practice(self):
        self.flow.first_begin()
        self.acknowledge_stage()
        self.assertEqual(self.runtime.screens, ["selRoom"])
        self.assertIn(("NewPlayerPractice",), self.runtime.calls)
        self.assertEqual(self.runtime.boxes, [])
        self.assertFalse(self.flow.first or self.flow.tutorial)

    def test_repeated_tutorial_asks_for_the_recordings(self):
        self.flow.begin()
        self.acknowledge_stage()
        self.assertEqual(self.runtime.boxes, [(ASK_RECORDS_TEXT, 0, ASK_RECORDS)])
        self.assertIn(("ui_msgBox", 0), self.runtime.calls)
        self.assertNotIn(("NewPlayerPractice",), self.runtime.calls)

    def test_cancel_plays_nothing(self):
        self.assertTrue(self.flow.on_msgbox_result(ASK_RECORDS, 0))
        self.assertIsNone(self.runtime.activity)
        self.assertFalse(self.flow.on_msgbox_result((2 << 24) | 1, 1))

    def test_recordings_play_in_order_then_return_to_the_lobby(self):
        self.flow.on_msgbox_result(ASK_RECORDS, 1)
        for expected in ("tutorial1.qbv", "tutorial2.qbv", "tutorial3.qbv"):
            self.runtime.run_posted()
            self.assertEqual(FakeReplay.started[-1].lower(), expected)
            self.runtime.activity.end(self.runtime)
        self.runtime.run_posted()
        self.assertEqual(len(FakeReplay.started), 3)
        self.assertEqual(self.runtime.screens, ["selRoom"])
        self.assertEqual((self.flow.records, self.flow.counter), (False, 0))
        self.assertEqual(sum(1 for call in self.runtime.calls if call[0] == "ui_NotifyShowLevelIcon"), 16)

    def test_natives_wrap_the_message_box_result(self):
        seen = []
        self.runtime.namespace["OnMsgBoxResult"] = lambda handle, ok: seen.append((handle, ok))
        _flow, natives = build_tutorial_natives(self.runtime, None, types.SimpleNamespace(root=pathlib.Path(".")))
        natives["OnMsgBoxResult"]((2 << 24) | 1, 1)
        self.assertEqual(seen, [((2 << 24) | 1, 1)])
        natives["OnMsgBoxResult"](ASK_RECORDS, 0)
        self.assertEqual(len(seen), 1)

    def test_record_names_match_case_insensitively(self):
        path = find_record(pathlib.Path(self.directory.name), "tutorial2.qbv")
        self.assertTrue(path.is_file())
        self.assertEqual(path.name.lower(), "tutorial2.qbv")


if __name__ == "__main__":
    unittest.main()
