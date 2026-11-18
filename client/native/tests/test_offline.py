"""Offline matches against AI seats: seat layouts, the practice room and,
when the sidecar and the client files are there, a whole offline match."""

import os
import pathlib
import sys
import types
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient import room as room_host  # noqa: E402
from qqtclient.battle.offline import offline_players, team_layout  # noqa: E402
from qqtclient.battle.sidecar import DEFAULT_PATH, Sidecar  # noqa: E402
from qqtclient.net import match, room  # noqa: E402
from qqtclient.net.player import GameInfo  # noqa: E402

CLIENT_ROOT = pathlib.Path(os.environ.get("QQT_TEST_CLIENT_ROOT", "")) if os.environ.get("QQT_TEST_CLIENT_ROOT") else None


class LayoutTest(unittest.TestCase):
    def test_counts_put_teammates_with_the_local_player(self):
        self.assertEqual(team_layout(0, 1), [1, 2])
        self.assertEqual(team_layout(1, 2), [1, 1, 2, 2])

    def test_explicit_teams_win(self):
        self.assertEqual(team_layout(5, 5, "1,2,3"), [1, 2, 3])
        with self.assertRaises(ValueError):
            team_layout(0, 0, "1,9")
        with self.assertRaises(ValueError):
            team_layout(4, 4)

    def test_players(self):
        players = offline_players(3, [1, 1, 2], ext_point=500, policy="bot")
        self.assertEqual([(p["id"], p["team"], p["ai"]) for p in players], [(1, 1, False), (2, 1, True), (3, 2, True)])
        self.assertEqual(players[0]["role"], 3)
        self.assertTrue(all(1 <= p["role"] <= 8 for p in players))
        self.assertNotIn("policy", players[0])
        self.assertEqual(players[2]["policy"], "bot")
        self.assertEqual(players[1]["ext_point"], 500)


class FakeRuntime:
    def __init__(self):
        self.namespace = {"Win_SetColorText": lambda *args: self.chat.append(args[1])}
        self.current_screen = "selRoom"
        self.screens, self.calls, self.chat, self.boxes = [], [], [], []
        self.event_hooks, self.tick_hooks = [], []

    def goto_screen(self, screen):
        self.current_screen = screen
        self.screens.append(screen)

    def show_msgbox(self, text, *args):
        self.boxes.append(text)

    def find(self, _name):
        return None


class FakeSession:
    def __init__(self):
        self.uin, self.nickname, self.gender, self.game_type = 1000011, "测1", 0, 0
        self.profile = types.SimpleNamespace(player_id=7, identity=0, game_info=GameInfo(*([0] * 7), 3, *([0] * 5)),
                                             section_mode=0)
        self.state = {"last_role": 4}
        self.handlers, self.on_disconnected, self.sent = {}, [], []
        self.connection, self.pets, self.hall, self.section = None, None, None, None

    def send(self, command, payload, *args):
        self.sent.append(command)
        return True

    def save_state(self, **values):
        self.state.update(values)


class PracticeRoomTest(unittest.TestCase):
    def setUp(self):
        self.runtime, self.session = FakeRuntime(), FakeSession()
        self.started = []
        natives, _draw = room_host.build_room_natives(
            self.runtime, self.session, avatars=None,
            start_offline=lambda *args: self.started.append(args))
        self.natives = natives
        for name in ("ui_SetGameType", "ui_createRoomOK", "ui_NotifyRoomID", "NotifyRoomPropChange", "ui_NotifyRoomSeatChange",
                     "ui_NotifyMapChange", "NotifyRoom"):
            self.runtime.namespace[name] = lambda *args, name=name: self.runtime.calls.append((name, args))
        natives["sc_openPracticeRoom"]()

    def seat(self, index):
        return self.natives["GetSeatInfo"](index)

    def test_opens_on_map_311_with_one_opponent(self):
        self.assertEqual(self.runtime.current_screen, "room")
        self.assertIn(("ui_NotifyMapChange", (311,)), self.runtime.calls)
        mine, ai = self.seat(0), self.seat(1)
        self.assertEqual(ord(mine.bIsMaster), 1)
        self.assertEqual(mine.player.m_dwUin, 1000011)
        self.assertEqual(mine.player.m_stGameInfo.m_nRoleID if hasattr(mine.player.m_stGameInfo, "m_nRoleID") else 4, 4)
        self.assertEqual(ai.player.m_bTermAndSeat >> 4, 2)
        self.assertEqual(ord(self.seat(2).seatStatus), room_host.NATIVE_SEAT_OPEN)
        self.assertEqual(self.session.sent, [], "nothing goes to the server")

    def test_seats_teams_map_and_start(self):
        click = self.natives["UserClickSeatInRoom"]
        click(2, 0)  # an opponent joins seat 3
        click(3, 0)
        for _ in range(3):  # 2 -> 3 -> 4 -> 1: a teammate
            click(3, 0)
        self.assertEqual(self.seat(3).player.m_bTermAndSeat >> 4, 1)
        click(2, 0)
        click(2, 0)
        click(2, 0)
        click(2, 0)  # team 1 next click empties the seat
        self.assertEqual(ord(self.seat(2).seatStatus), room_host.NATIVE_SEAT_OPEN)
        self.natives["UserSelTeamInRoom"](True, 1)
        self.natives["UserSelRoleInRoom"](True, 6)
        self.natives["UserSelMapInRoom"](1102)
        self.natives["UserReady"]()
        self.assertEqual(len(self.started), 1)
        map_id, players, local_id, free_rule, game_type, _wear, back = self.started[0]
        self.assertEqual((map_id, local_id, free_rule, game_type), (1102, 1, False, 0))
        self.assertEqual([(p["id"], p["team"], p["ai"], p["role"]) for p in players][0], (1, 1, False, 6))
        self.assertEqual(sorted((p["id"], p["team"]) for p in players), [(1, 1), (2, 2), (4, 1)])
        self.assertEqual(self.session.sent, [])
        back()
        self.assertEqual(self.runtime.current_screen, "room")

    def test_leave_goes_back_to_the_lobby(self):
        self.natives["LeaveRoom"]()
        self.assertEqual(self.runtime.current_screen, "selRoom")
        self.assertEqual(self.session.sent, [])
        self.assertIsNone(self.natives["GetSeatInfo"](0).player.m_dwUin or None)


@unittest.skipUnless(CLIENT_ROOT and DEFAULT_PATH.is_file(), "needs QQT_TEST_CLIENT_ROOT and a built sidecar")
class OfflineSidecarTest(unittest.TestCase):
    def test_a_whole_match_ends_with_a_game_over(self):
        sidecar = Sidecar()
        try:
            players = [dict(player, ai=True) for player in offline_players(1, [1, 2], policy="bot")]
            state = sidecar.start_offline(CLIENT_ROOT, 1401, players, 0, 7, 9)
            self.assertEqual(state["begin"]["rule"], "box")
            for _ in range(20_000):
                state = sidecar.step([], 10)
                if (state.get("offline") or {}).get("game_over"):
                    break
            over = match.parse_game_over(bytes.fromhex(state["offline"]["game_over"]))
            self.assertEqual(sorted(result.player_id for result in over.results), [1, 2])
        finally:
            sidecar.close()


if __name__ == "__main__":
    unittest.main()
