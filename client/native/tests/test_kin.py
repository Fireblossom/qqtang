import struct
import types
import unittest

from qqtclient import kin as kin_host
from qqtclient.net import kin


def base_bytes(index=9, owner=1000005, name="糖家族", section=1, title=b"", list_update=3, notification="", status=200 << 20):
    content = "宣言".encode("gbk")
    out = struct.pack(">6I", index, owner, 1, 0, status, 0) + struct.pack(">II", 0, 7)
    out += struct.pack(">HHH", 2, len(content), len(title)) + name.encode("gbk").ljust(17, b"\0") + content + title
    note = notification.encode("gbk") or b"\0"
    out += struct.pack(">III", section, 5, list_update) + struct.pack(">H", len(note)) + note
    return out + struct.pack(">8I", 0, 0, 40, 0, 3, 0, 4, 0)


def member_bytes(uin, nick, grade, online):
    nickname = nick.encode("gbk")
    return struct.pack(">IH", uin, len(nickname)) + nickname + struct.pack(">6I", 0, (grade << 24) | online, 0, grade, 0, 0)


class KinTest(unittest.TestCase):
    def setUp(self) -> None:
        self.sent, self.calls, self.lines = [], [], []
        namespace = {"GetPlayerCnt": lambda mode=0: 0, "fetch_PlayerInfoInPlayerList": lambda i, mode=0: None, "getExtItem": lambda *a: 0,
                     "GetPlayerInfoByUin": lambda uin: types.SimpleNamespace(m_dwPlayerUin=0),
                     "Win_SetColorText": lambda widget, text, *_: self.lines.append(text)}
        for name in ("ui_updateMemberList", "updatePlayer", "ShowkinInfoDlg", "updateKinManagerDlg", "ui_updateTopkinList"):
            namespace[name] = lambda *args, name=name: self.calls.append(name)
        runtime = types.SimpleNamespace(namespace=namespace, current_screen="selRoom", find=lambda path: object(),
                                        show_msgbox=lambda *args: self.calls.append(("msgbox", args[0])))
        self.session = types.SimpleNamespace(uin=1000005, handlers={}, on_entered=[], on_disconnected=[],
                                             send=lambda command, payload: self.sent.append((command, payload)) or True)
        self.natives = kin_host.build_kin_natives(runtime, self.session)
        self.state = self.session.kin

    def deliver(self, command, payload):
        self.session.handlers[command](types.SimpleNamespace(payload=payload))

    def test_login_restore_fetches_base_then_members(self) -> None:
        self.deliver(kin.CREATE_COMMAND, struct.pack(">IIIH", 1000005, 9, 0, 0))
        self.assertEqual((self.natives["GetKinParam"](0), self.sent[-1][0]), (9, kin.FETCH_BASE_COMMAND))
        self.assertEqual(struct.unpack(">4I", self.sent[-1][1]), (1000005, 0, 9, 1))
        title = kin.encode_titles(["族长", "长老", "精英", "成员", "新人"])
        self.deliver(kin.FETCH_BASE_COMMAND, struct.pack(">I", 1000005) + base_bytes(title=title))
        self.assertEqual((self.sent[-1][0], self.natives["GetKinPosition"](1)), (kin.FETCH_MEMBERS_COMMAND, "长老"))
        info = self.natives["GetInviteKinInfo"](1)
        self.assertEqual((info.m_szName, info.m_wMemberNum, info.m_dwStatus >> 20, info.m_stKinFlagID.m_iFlagID), ("糖家族", 2, 200, 7))
        members = member_bytes(1000006, "糖六", 4, 0) + member_bytes(1000005, "糖五", 12, 1)
        self.deliver(kin.FETCH_MEMBERS_COMMAND, struct.pack(">IIHH", 1000005, 3, 0, 2) + members + struct.pack(">4I", 5, 1, 9, 2))
        self.assertEqual(self.calls[-2:], ["ui_updateMemberList", "updatePlayer"])
        first = self.natives["fetch_PlayerInfoInPlayerList"](0, 2).m_PlayerInfo
        self.assertEqual((first.m_dwPlayerUin, self.natives["GetPlayerCnt"](3)), (1000005, 1))  # online first
        self.assertEqual((self.natives["GetKinMemberHonor"](1000005, 1), self.natives["GetKinPositionByUin"](1000006)), (12, "新人"))
        self.natives["DoKinTask"]("Y", "新人", 4, 1000006, 2)  # promote 新人 -> 成员 (grade 6)
        self.assertEqual(struct.unpack(">5I", self.sent[-1][1])[3:], (1000006, 6))
        self.natives["DoKinTask"]("", "", 0, 0, 6)
        self.assertEqual(self.lines[-1], "族长不能退出家族.")

    def test_invitation_waits_in_the_envelope_and_answers_the_owner(self) -> None:
        words = []
        self.session.push_script_word = words.append
        self.deliver(kin.SERVER_OPERATE_COMMAND, struct.pack(">6I", 1000007, 1, 9, 1000005, 0, 0) + base_bytes(owner=1000001))
        self.assertEqual(words, ["ShowkininviteDlg"])
        self.natives["DoKinTask"]("", "Y", 1, 0, 11)
        self.assertEqual(struct.unpack(">6I", self.sent[-1][1])[:5:2], (1000001, 9, 0))

    def test_rankings_tolerate_a_missing_group_size(self) -> None:
        record = struct.pack(">IB", 9, 3) + b"abc" + struct.pack(">IHH", 50, 1, 2)
        empty = struct.pack(">IBIHH", 0, 0, 0, 0, 0)
        payload = struct.pack(">HIII", 0, 1, 1000005, 77) + record + empty * 39 + struct.pack(">HH", 0, 0)
        self.deliver(kin.FETCH_TOP_COMMAND, payload)
        row = self.natives["fetch_topkininfolist"](1, 1, 0)
        self.assertEqual((row.m_szName, row.m_iValue, row.m_nOrder, row.m_nAscend, self.calls[-1]), ("abc", 50, "1", 1, "ui_updateTopkinList"))


if __name__ == "__main__":
    unittest.main()
