import struct
import types
import unittest
from unittest import mock

from qqtclient import shop as host, chat as chat_host
from qqtclient.net import shop, chat


class PresentTest(unittest.TestCase):
    def test_gift_note_is_visible_while_recipient_is_in_shop(self):
        messages = []
        runtime = types.SimpleNamespace(current_screen="shop", namespace={},
                                        show_msgbox=lambda text, *args: messages.append(text))
        session = types.SimpleNamespace(uin=1000002, handlers={}, shielded=set(), on_disconnected=[])
        chat_host.build_chat_natives(runtime, session)
        content = "wisper_uin(1000001)_content[4](礼物)".encode("gbk")
        payload = struct.pack(">IH", 1000001, len(content)) + content + "糖一".encode("gbk").ljust(32, b"\0")
        session.handlers[chat.WHISPER_NOTIFY_COMMAND](types.SimpleNamespace(payload=payload))
        self.assertEqual(messages, ["糖一悄悄对你说:礼物"])

    def test_compact_request_matches_native_040e_layout(self):
        self.assertEqual(shop.build_present(1000001, 1000002, 20050, "礼物", 3, 848, True, 123), bytes.fromhex(
            "000f4241 0000007b 00004e52 0002 0003 000f4242 04 c0f1ceef 00000350 0001"))
        payload = shop.build_present(1000001, 1000002, 20050, "糖" * 200, 3, 848, now=123)
        self.assertEqual(payload[20], 254)  # never split a GBK character or wrap the byte count
        self.assertEqual(len(payload), 27 + 254)

    def test_response_and_absolute_balance_use_separate_messages(self):
        calls, messages, sent = [], [], []
        response = struct.pack(">H", 0) + "赠送成功".encode("gbk").ljust(128, b"\0") + struct.pack(">HHI", 2, 3, 1000)
        self.assertEqual(shop.parse_present(response), (0, "赠送成功", 1000))
        runtime = types.SimpleNamespace(current_screen="shop", namespace={"ui_NotifyMoney": lambda *args: calls.append(args)},
                                        post=lambda fn: fn(), show_msgbox=lambda text, *args: messages.append(text))
        hall = types.SimpleNamespace(version=848, channels=[types.SimpleNamespace(sections=[types.SimpleNamespace(server_id=1)])],
                                     servers=[types.SimpleNamespace(id=1, ip="127.0.0.1", port=58000), types.SimpleNamespace(id=2, ip="127.0.0.1", port=58001)])
        session = types.SimpleNamespace(uin=1000001, handlers={}, hall=hall, section=types.SimpleNamespace(id=1),
                                       profile=types.SimpleNamespace(items=[], game_info=types.SimpleNamespace(money=2500)))
        natives, _ = host.build_shop_natives(runtime, session, None, None)
        def request(address, uin, section, command, payload):
            sent.append((command, payload))
            return types.SimpleNamespace(payload=response)
        with mock.patch.object(host.threading, "Thread", lambda target, **kwargs: types.SimpleNamespace(start=target)), mock.patch.object(shop, "request", request):
            natives["PresentCommodity"](1000002, 20050, 4, "礼物", 2, 3, 0)
        self.assertEqual(sent[0][0], shop.PRESENT_COMMAND)
        self.assertEqual(messages, ["赠送成功"])
        self.assertEqual(session.profile.game_info.money, 2500)  # no guessed subtraction
        session.handlers[shop.MONEY_NOTIFY_COMMAND](types.SimpleNamespace(payload=struct.pack(">HBII", 0, 3, 1500, 1000001)))
        self.assertEqual(session.profile.game_info.money, 1500)
        self.assertEqual(calls, [(0, 0, 1500)])
        session.handlers[shop.MONEY_NOTIFY_COMMAND](types.SimpleNamespace(payload=struct.pack(">HBII", 0, 3, 17, 1000002)))
        self.assertEqual(session.profile.game_info.money, 1500)
