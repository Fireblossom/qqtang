import struct
import types
import unittest

from qqtclient import marriage as marriage_host
from qqtclient.net import marriage
from qqtclient.roomwalk import Couple


def nick(text):
    return text.encode("gbk").ljust(20, b"\0")


def text8(text):
    data = text.encode("gbk")
    return bytes([len(data)]) + data


def info_payload(target, spouse, word="爱你", spouse_nick="糖六", ring=9021):
    data = word.encode("gbk")
    return (struct.pack(">HIIIHIIH", 0, target, spouse, 30, 2, 100, 5, len(data)) + data
            + nick("糖五") + nick(spouse_nick) + struct.pack(">I", ring))


class MarriageTest(unittest.TestCase):
    def setUp(self) -> None:
        self.sent, self.calls, self.texts, self.sounds, self.words = [], [], {}, [], []
        namespace = {"FindFriend": lambda uin: self.calls.append(("find", uin)),
                     "Win_SetText": lambda widget, text: self.texts.__setitem__(widget, text),
                     "PlaySound": lambda name, loops: self.sounds.append(name)}
        for name in ("ReceiveSpark", "ui_UpdateMyMarriageInfo", "SetWeddingStart", "ReceiveMarriageConfirm",
                     "MyWeddingOver", "WeddingOver", "RestartWedding", "UpdateWeddingMode", "UpdateLoveword", "ui_msgBox"):
            namespace[name] = lambda *args, name=name: self.calls.append((name,) + args)
        self.runtime = types.SimpleNamespace(namespace=namespace, current_screen="selRoom",
                                             show_msgbox=lambda *args: self.calls.append(("msgbox", args[0])))
        room = types.SimpleNamespace(seats={1000006: 2})
        self.session = types.SimpleNamespace(uin=1000005, handlers={}, on_entered=[], on_disconnected=[],
                                             push_leave_word=self.words.append, room_view=lambda: room,
                                             send=lambda command, payload: self.sent.append((command, payload)) or True)
        self.natives = marriage_host.build_marriage_natives(self.runtime, self.session)

    def deliver(self, command, payload):
        self.session.handlers[command](types.SimpleNamespace(payload=payload))

    def test_login_and_find_friend_ask_for_the_record(self) -> None:
        self.session.on_entered[0]()
        self.assertEqual(self.sent[-1], (marriage.INFO_COMMAND, struct.pack(">II", 1000005, 1000005)))
        self.natives["FindFriend"](1000006)
        self.assertEqual(self.sent[-1][1], struct.pack(">II", 1000005, 1000006))
        self.assertEqual(self.calls[-1], ("find", 1000006))

    def test_info_fills_own_or_other_cache(self) -> None:
        self.deliver(marriage.INFO_COMMAND, info_payload(1000005, 1000006))
        mine = self.natives["GetMyMarriageDetail"]()
        self.assertEqual((mine.m_dwMarriageUin, mine.m_szSpouseNickname, mine.m_szLoveWord, mine.m_nLoveWordLength, mine.m_dwRingID),
                         (1000006, "糖六", "爱你", 4, 9021))
        self.assertEqual(self.calls[-1], ("ui_UpdateMyMarriageInfo",))
        count = len(self.calls)
        self.deliver(marriage.INFO_COMMAND, info_payload(1000007, 1000008, spouse_nick="糖八"))
        self.assertEqual((self.natives["GetMarriageDetail"]().m_dwMarriageUin, len(self.calls)), (1000008, count))
        # SaveLoveWord and Divorce go to the cached spouse.
        self.natives["SaveLoveWord"](4, "永远")
        self.assertEqual(self.sent[-1][1], struct.pack(">IIH", 1000005, 1000006, 4) + "永远".encode("gbk"))

    def test_spark_round_trip(self) -> None:
        self.natives["Spark"](99, "嫁给我吧", 1000006)
        self.assertEqual(self.sent[-1], (marriage.SPARK_COMMAND, struct.pack(">IIB", 1000005, 1000006, 8) + "嫁给我吧".encode("gbk")))
        self.deliver(marriage.SPARK_NOTIFY_COMMAND, struct.pack(">II", 1000006, 1000005) + nick("糖六") + text8("你好"))
        self.assertEqual((self.texts["UI.TextBuffer"], self.texts["UI.TextBuffer2"], self.calls[-1], self.sounds[-1]),
                         ("你好", "糖六", ("ReceiveSpark", 1000006), "sparkCome.wav"))
        self.natives["AnswerSpark"](0, 1000006)
        self.assertEqual(self.sent[-1][1], struct.pack(">IIH", 1000005, 1000006, 0))
        # Success still plays sparkFail.wav; during a match it is an envelope line.
        self.deliver(marriage.ANSWER_SPARK_COMMAND, struct.pack(">HII", 0, 1000006, 1000005) + text8("求婚成功"))
        self.assertEqual((self.calls[-2:], self.sounds[-1]), ([("msgbox", "求婚成功"), ("ui_msgBox", 3)], "sparkFail.wav"))
        self.runtime.current_screen = "game"
        self.deliver(marriage.SPARK_COMMAND, struct.pack(">HII", 2, 1000005, 1000006) + text8("对方当前不在线"))
        self.assertEqual(self.words, ["对方当前不在线"])

    def test_wedding_flow(self) -> None:
        start = struct.pack(">II", 1000005, 1000006) + nick("糖五") + nick("糖六") + struct.pack(">H", 0) + text8("请确认婚礼") + struct.pack(">H", 1)
        self.deliver(marriage.START_WEDDING_COMMAND, start)
        self.assertEqual((self.calls[-1], self.texts["UI.TextBuffer2"]), (("SetWeddingStart", 1, 1000005, 1000006), "糖六"))
        self.deliver(marriage.WEDDING_CONFIRM_COMMAND, struct.pack(">II", 1000005, 1000006) + nick("糖五") + nick("糖六") + text8("请确认婚礼"))
        self.assertEqual((self.calls[-1], self.texts["UI.TextBuffer"]), (("ReceiveMarriageConfirm",), "请确认婚礼"))
        answer = struct.pack(">II", 1000006, 1000005) + nick("糖六") + nick("糖五") + struct.pack(">H", 0) + text8("婚礼开始")
        self.deliver(marriage.ANSWER_WEDDING_COMMAND, answer)
        self.assertEqual((self.calls[-1], self.texts["UI.TextBuffer3"]), (("MyWeddingOver",), "婚礼开始"))
        self.assertEqual(self.sent[-1], (marriage.INFO_COMMAND, struct.pack(">II", 1000005, 1000005)))
        self.session.uin = 1000007  # a guest sees the priest
        self.deliver(marriage.ANSWER_WEDDING_COMMAND, answer)
        self.assertEqual(self.calls[-1], ("WeddingOver", 1000006, 1000005))
        self.assertEqual(self.natives["GetSeatIDByUin"](1000006), 2)
        self.assertEqual(self.natives["GetSeatIDByUin"](1), -1)

    def test_failed_wedding_answer_restarts_for_the_couple(self) -> None:
        answer = struct.pack(">II", 1000006, 1000005) + nick("糖六") + nick("糖五") + struct.pack(">H", 1) + text8("对方拒绝了婚礼")
        self.deliver(marriage.ANSWER_WEDDING_COMMAND, answer)
        self.assertEqual(self.calls[-3:], [("msgbox", "对方拒绝了婚礼"), ("ui_msgBox", 3), ("RestartWedding",)])

    def test_divorce_by_us_and_by_the_spouse(self) -> None:
        self.deliver(marriage.INFO_COMMAND, info_payload(1000005, 1000006))
        self.natives["Divorce"]()
        self.assertEqual(self.sent[-1], (marriage.DIVORCE_COMMAND, struct.pack(">II", 1000005, 1000006)))
        self.deliver(marriage.DIVORCE_COMMAND, struct.pack(">IIH", 1000005, 1000006, 0))
        self.assertEqual((self.calls[-2][1], self.sounds[-1], self.sent[-1][0]), ("离婚成功", "divorceOK.wav", marriage.INFO_COMMAND))
        self.deliver(marriage.DIVORCE_COMMAND, struct.pack(">IIH", 1000006, 1000005, 0))
        self.assertEqual(self.words, ["糖六与你离婚了！"])

    def test_mode_messages(self) -> None:
        self.deliver(marriage.MODE_NOTIFY_COMMAND, struct.pack(">IH", 1000006, 1))
        self.assertEqual(self.calls[-1], ("UpdateWeddingMode", 1))
        self.deliver(marriage.CHANGE_MODE_COMMAND, struct.pack(">IH", 1000005, 1) + text8("配偶不在婚礼房间里"))
        self.assertEqual(self.calls[-2], ("msgbox", "配偶不在婚礼房间里"))


class CoupleTest(unittest.TestCase):
    def test_love_effect_only_when_close_and_rechecked_every_two_seconds(self) -> None:
        a, b = types.SimpleNamespace(x=100, y=200), types.SimpleNamespace(x=149, y=230)
        couple = Couple(0, 1)
        couple.update(a, b, 16)
        self.assertEqual((couple.active, couple.at), (True, (124, 145)))
        b.x = 300
        couple.update(a, b, 1000)
        self.assertTrue(couple.active)  # still inside the 2 s window
        couple.update(a, b, 1000)
        couple.update(a, b, 16)
        self.assertFalse(couple.active)


if __name__ == "__main__":
    unittest.main()
