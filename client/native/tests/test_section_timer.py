"""The section timer's bugle queue and list redraw, room avatar clicks and
the section logout (docs/section-timer-re.md)."""

from __future__ import annotations

import os
import struct
import types
import unittest

os.environ.setdefault("SDL_VIDEODRIVER", "dummy")

import pygame  # noqa: E402

from qqtclient import roomwalk, session as session_host  # noqa: E402
from qqtclient.net import chat, player  # noqa: E402
from qqtclient.section_timer import Bugle, BugleQueue, SectionTimer, split_bugle  # noqa: E402


def recorder():
    calls = []
    return calls, lambda name, *args: calls.append((name, *args))


class SplitTest(unittest.TestCase):
    def test_name_before_marker_and_marker_stays_with_text(self):
        self.assertEqual(split_bugle("糖一说:大家好\r\n", 1000014), ("糖一", "说:大家好"))

    def test_no_name_uses_the_marker(self):
        self.assertEqual(split_bugle("没有标记", 1), ("说:", "没有标记"))
        self.assertEqual(split_bugle("说:开头", 1), ("说:", "说:开头"))

    def test_name_limit_counts_escaped_bytes(self):
        # 9 GBK characters (18 bytes) fit; one quote more escapes to 20 bytes.
        self.assertEqual(split_bugle("一二三四五六七八九说:x", 1)[0], "一二三四五六七八九")
        self.assertEqual(split_bugle("一二三四五六七八九'说:x", 1), ("说:", "一二三四五六七八九'说:x"))

    def test_inner_newline_and_official_viewer(self):
        self.assertEqual(split_bugle("甲说:a\nb", 1), ("甲", "说:a b"))
        self.assertEqual(split_bugle("甲说:a", 1000014, 0x10000), ("[1000014]", "甲说:a"))


class BugleQueueTest(unittest.TestCase):
    def test_one_bugle_timing(self):
        calls, call = recorder()
        queue = BugleQueue(call)
        queue.push(Bugle(7, "甲", "说:a", 0))
        queue.tick()
        self.assertEqual(calls, [])  # every second tick while fewer than 10 wait
        queue.tick()
        self.assertEqual(calls, [("NotifyRemovePopo",), ("NotifyBroadcast", "甲", 7, "说:a"), ("NotifyAddPopo", 0)])
        events = {}
        for tick in range(1, 61):
            calls.clear()
            queue.tick()
            for name, *_ in calls:
                events.setdefault(name, []).append(tick)
        # The bubble goes 2.5 s after the last bugle (then every 3 s); the
        # three slots 27.5, 28.5 and 29.5 s after it.
        self.assertEqual(events["NotifyRemovePopo"][:2], [5, 11])
        self.assertEqual(events["NotifyRemoveMsg"], [55, 57, 59])

    def test_backlog_shows_one_per_tick_and_keeps_50(self):
        calls, call = recorder()
        queue = BugleQueue(call)
        for index in range(60):
            queue.push(Bugle(index, "n", "t", 0))
        self.assertEqual(len(queue.entries), 50)
        self.assertEqual(queue.entries[0].uin, 10)
        shown = []
        for _ in range(44):
            calls.clear()
            queue.tick()
            shown.append(sum(1 for name, *_ in calls if name == "NotifyBroadcast"))
        self.assertEqual(shown[:41], [1] * 41)  # 50 down to 9
        # The tick that drops below 10 resets and steps the alternation, so
        # the next one shows too; then every second tick.
        self.assertEqual(shown[41:], [1, 0, 1])
        self.assertEqual(queue.countdowns, [-1, -1, -1])


class SectionTimerTest(unittest.TestCase):
    def test_lists_every_second_and_ticks_coalesce(self):
        calls, call = recorder()
        connected = [True]
        timer = SectionTimer(call, lambda: connected[0])
        timer.advance(1000)
        self.assertEqual(calls, [])  # not started before a section login
        timer.start()
        timer.advance(499)
        timer.advance(1)
        self.assertEqual(calls, [])
        timer.advance(500)
        self.assertEqual(calls, [("updatePlayer",), ("updatePlayer2",)])
        calls.clear()
        timer.advance(5000)  # one WM_TIMER for a long frame
        timer.advance(500)
        self.assertEqual(calls, [("updatePlayer",), ("updatePlayer2",)])
        connected[0] = False
        calls.clear()
        timer.advance(1000)
        self.assertEqual(calls, [])


class SectionMessageTest(unittest.TestCase):
    def test_effect_and_whole_line(self):
        line = "甲说:你好".encode("gbk")
        payload = struct.pack(">HHH", 1, 0xFFFF, len(line)) + line + struct.pack(">IIII", 1000014, 0x4000, 30123, 0) + bytes(8)
        message = chat.parse_section_message(payload)
        self.assertTrue(message.bugle)
        self.assertEqual((message.line, message.effect, message.uin), ("甲说:你好", 30123, 1000014))

    def test_logout_request_layout(self):
        self.assertEqual(player.build_logout_request(1000014, now=0x01020304), bytes.fromhex("000f424e 01020304 00"))


class FakeConnection:
    def __init__(self):
        self.sent, self.closed = [], False

    def send(self, command, payload, route=2):
        self.sent.append((command, payload))

    def close(self):
        self.closed = True


def fake_runtime(namespace):
    return types.SimpleNamespace(namespace=namespace, tick_hooks=[], event_hooks=[], quit_requested=False,
                                 show_msgbox=lambda *args: None, post=lambda function: function())


class LogoutTest(unittest.TestCase):
    def test_logout_sends_0065_then_closes(self):
        calls = []
        runtime = fake_runtime({"NotifyRemoveMsg": lambda: calls.append("NotifyRemoveMsg")})
        session = session_host.Session("127.0.0.1")
        natives = session_host.build_session_natives(runtime, session)
        connection = FakeConnection()
        session.connection, session.profile, session.uin = connection, types.SimpleNamespace(identity=0), 1000014
        natives["LogoutSection"](1000014)
        self.assertEqual(calls, ["NotifyRemoveMsg"])
        self.assertEqual([command for command, _ in connection.sent], [player.LOGOUT_COMMAND])
        self.assertEqual(connection.sent[0][1][:4], struct.pack(">I", 1000014))
        self.assertTrue(connection.closed)
        self.assertIsNone(session.connection)
        natives["LogoutSection"](1000014)  # not logged in: nothing is sent
        self.assertEqual(len(connection.sent), 1)

    def test_close_and_alt_f4_run_leave_section(self):
        calls = []
        runtime = fake_runtime({"leaveSection": lambda: calls.append("leaveSection")})
        session_host.build_session_natives(runtime, session_host.Session("127.0.0.1"))
        hook = runtime.event_hooks[0]
        hook(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_F4, mod=pygame.KMOD_LALT))
        self.assertTrue(runtime.quit_requested)
        hook(pygame.event.Event(pygame.QUIT))  # already leaving
        self.assertEqual(calls, ["leaveSection"])
        runtime.quit_requested = False
        hook(pygame.event.Event(pygame.QUIT))
        self.assertEqual(calls, ["leaveSection", "leaveSection"])


class AvatarClickTest(unittest.TestCase):
    def make(self, game_type):
        calls = []
        namespace = {"ShowKickoutDlg": lambda seat: calls.append(("kick", seat)),
                     "chatWithPlayerInChatRoom": lambda seat: calls.append(("chat", seat))}
        runtime = types.SimpleNamespace(namespace=namespace, event_hooks=[], tick_hooks=[], current_screen="room",
                                        activity=None, focus=None, last_press=(1.0, (0, 0), None))
        session = types.SimpleNamespace(on_disconnected=[])
        avatar = lambda x, y: types.SimpleNamespace(role=1, team=0, x=x, y=y)  # noqa: E731
        state = types.SimpleNamespace(snapshot=types.SimpleNamespace(game_type=game_type, local_seat=1),
                                      avatars=[avatar(100, 200), types.SimpleNamespace(role=0, team=-1, x=0, y=0), avatar(110, 210)],
                                      members={})
        return roomwalk.RoomWalk(runtime, session, state, None, None), calls, runtime

    def press(self, walk, button, pos):
        return walk.handle(pygame.event.Event(pygame.MOUSEBUTTONDOWN, button=button, pos=pos))

    def test_right_press_on_avatar_in_chat_room(self):
        walk, calls, _ = self.make(roomwalk.CHAT_ROOM_TYPE)
        self.assertTrue(self.press(walk, 3, (120, 190)))  # seat 0's box comes first
        self.assertTrue(self.press(walk, 3, (125, 190)))  # x+25 is outside seat 0, seat 2 takes it
        self.assertFalse(self.press(walk, 3, (110, 210)))  # y itself is outside
        self.assertFalse(self.press(walk, 3, (100, 135)))  # so is y-65
        self.assertEqual(calls, [("kick", 0), ("kick", 2)])

    def test_left_double_click_and_other_rooms(self):
        walk, calls, runtime = self.make(roomwalk.CHAT_ROOM_TYPE)
        self.assertFalse(self.press(walk, 1, (110, 190)))
        self.assertTrue(self.press(walk, 1, (111, 190)))
        self.assertEqual(calls, [("chat", 0)])
        self.assertEqual(runtime.last_press, (0.0, (0, 0), None))
        self.assertFalse(self.press(walk, 1, (111, 190)))  # a third press starts over
        walk, calls, _ = self.make(1)
        self.assertFalse(self.press(walk, 3, (100, 190)))
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
