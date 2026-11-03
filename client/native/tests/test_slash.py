import types
import unittest

from qqtclient import slash


class AcrossFormatTest(unittest.TestCase):
    def test_round_trips(self) -> None:
        kind, fields = slash.parse_across(slash.whisper_content(1000005, "你好 _(x)"))
        self.assertEqual((kind, fields), ("wisper", {"uin": 1000005, "content": "你好 _(x)"}))
        kind, fields = slash.parse_across(slash.invite_content(1000006, 1, 2, 7, 3, "密码"))
        self.assertEqual(kind, "in")
        self.assertEqual(fields, {"uin": 1000006, "serverid": 1, "sectionid": 2, "roomid": 7, "roomflag": 3, "password": "密码"})
        self.assertEqual(slash.parse_across("inresult_resultid(2)"), ("inresult", {"resultid": 2}))
        self.assertEqual(slash.parse_across("plain words"), ("", {"content": "plain words"}))


class CommandTest(unittest.TestCase):
    def setUp(self) -> None:
        self.lines, self.calls, self.sent, self.whispers = [], [], [], []
        namespace = {"Win_SetColorText": lambda widget, text, *_: self.lines.append(text)}
        for name in ("UpdateUIWisper", "FindFriend", "EnterRoom", "AutoJoinRoom", "DoFriendTask", "UserReady", "C2CInviteNegotiate"):
            namespace[name] = lambda *args, name=name: self.calls.append((name, *args))
        runtime = types.SimpleNamespace(namespace=namespace, current_screen="selRoom", find=lambda path: object(),
                                        show_msgbox=lambda *args: self.calls.append(("msgbox", *args)))
        self.session = types.SimpleNamespace(uin=1000005, profile=types.SimpleNamespace(identity=0), shielded=set(), room_names={3: "a", 1: "b"},
                                             room_view=lambda: None, section=None, state={}, save_state=lambda **v: self.session.state.update(v),
                                             send=lambda command, payload: self.sent.append((command, payload)) or True)
        self.target = {"uin": 0, "name": ""}
        self.commands = slash.build_slash(runtime, self.session, self.target, lambda uin, text: self.whispers.append((uin, text)))

    def test_commands(self) -> None:
        self.assertTrue(self.commands.handle("/w 1000006"))
        self.assertEqual(self.target["uin"], 1000006)
        self.assertTrue(self.commands.handle("/W off"))
        self.assertEqual(self.target["uin"], 0)
        self.commands.handle("/w 1000005")
        self.assertEqual(self.lines[-2:], ["不能和自己说悄悄话", slash.INVALID])
        self.commands.handle("/go 2")
        self.assertEqual(self.calls[-1], ("EnterRoom", 3, ""))  # the password never goes out
        self.commands.handle("/kick 1000006")
        self.assertEqual(self.lines[-1], slash.INVALID)
        self.assertFalse(self.commands.handle("/xin 你好"))  # an emote, so chat
        self.assertTrue(self.commands.handle("hi /m03"))  # member emote without membership
        self.commands.handle("/AR")
        self.assertEqual((self.session.state, self.lines[-1]), ({"autoready_1000005": True}, "设置自动准备"))

    def test_mute_and_reply(self) -> None:
        self.assertEqual(self.commands.receive(1000006, "糖六", slash.whisper_content(1000006, "hi")), "hi")
        self.commands.handle("/r back")
        self.assertEqual((self.whispers[-1], self.lines[-1]), ((1000006, "back"), "回复玩家糖六[1000006]: back"))
        self.commands.handle("/mute")
        self.assertIsNone(self.commands.receive(1000006, "糖六", slash.whisper_content(1000006, "hi")))

    def test_refused_invitations_are_answered(self) -> None:
        self.commands.handle("/refuse")
        self.assertIsNone(self.commands.receive(1000006, "糖六", slash.invite_content(1000006, 1, 1, 2, 0, "")))
        self.assertIn(b"inresult_resultid(1)", self.sent[-1][1])
        self.commands.handle("/unrefuse")
        self.commands.receive(1000006, "糖六", slash.invite_content(1000006, 1, 1, 2, 0, "pw"))
        box = self.calls[-1]
        self.assertEqual(box[0], "msgbox")
        box[5](True)
        self.assertEqual(self.calls[-1], ("EnterRoom", 2, "pw"))


if __name__ == "__main__":
    unittest.main()
