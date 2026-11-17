"""Native .qbv recordings: format round trip, save path and worn parts."""

import datetime
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient import qbv
from qqtclient.battle.replay import ai_record, record_path, recorded_wear
from qqtclient.net.player import GameInfo, ItemInfo, Reader
from qqtclient.net.room import RoomPlayer, encode_room_player, read_room_player


def member(items):
    info = GameInfo(*([0] * 13))
    info.role_id = 3
    return RoomPlayer(uin=1000001, nickname="糖一", player_id=7, team=2, seat=3, status=1, gender=1, icon_id=0, identity=0,
                      game_info=info, items=items, kin_index=0, kin_name="", kin_flag=b"\xff" * 8, pet=bytes(38))


class QbvTest(unittest.TestCase):
    def test_round_trip(self):
        player = member([ItemInfo(1005, 1, 1, 3, 0, 0, 0, 0)])
        recording = qbv.Recording(1000001, 7, b"\x00\x01begin", b"over", [], [
            qbv.Event(3000, 0x0FA2, b"\x00\x07move", (1, 3594, 0)), qbv.Event(3020, 0x0FA3, b"\x00\x07", (2, 3610, 0))])
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "a" / "b.qbv"
            qbv.write(path, recording, [encode_room_player(player)])
            loaded = qbv.read(path)
        self.assertEqual((loaded.local_uin, loaded.local_id, loaded.begin, loaded.over), (1000001, 7, b"\x00\x01begin", b"over"))
        self.assertEqual(loaded.events, recording.events)
        self.assertEqual(loaded.duration_ms, 3020)
        self.assertEqual((loaded.players[0].nickname, loaded.players[0].team, loaded.players[0].seat), ("糖一", 2, 3))

    def test_player_record_round_trip(self):
        player = member([ItemInfo(1005, 1, 1, 3, 2, 1, 5, 0), ItemInfo(30099, 9, 0, 0, 0, 0, 0, 0)])
        data = encode_room_player(player)
        self.assertEqual(encode_room_player(read_room_player(Reader(data))), data)
        ai = read_room_player(Reader(ai_record({"id": 20001, "role": 5, "team": 2, "seat": 1, "name": "AI1"})))
        self.assertEqual((ai.player_id, ai.game_info.role_id, ai.team, ai.nickname), (20001, 5, 2, "AI1"))

    def test_save_path_uses_the_native_name(self):
        path = record_path(pathlib.Path("/c"), 1000007, datetime.datetime(2026, 11, 17, 16, 48, 59))
        self.assertEqual(path, pathlib.Path("/c/Record/1000007/2026年11月17日16时48分.qbv"))

    def test_wear_comes_from_items_of_the_role(self):
        player = member([ItemInfo(1005, 1, 1, 3, 0, 0, 0, 0), ItemInfo(1008, 1, 1, 4, 0, 0, 0, 0), ItemInfo(1, 1, 1, 3, 0, 0, 0, 0)])
        namespace = {"itemList": {1005: ("cap", 12), 1008: ("cloth", 4), 1: ("bg", 2)}, "equipMap": {"cap": 1, "cloth": 9}}
        self.assertEqual(recorded_wear(player, 3, namespace), (("cap", 12),))


if __name__ == "__main__":
    unittest.main()
