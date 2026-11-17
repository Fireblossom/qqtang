"""TEXTLIST markup and hit test (gameUI.dll 0x100231AA, 0x10022CA3,
0x10024190) and the right button events (0x100304C8, 0x10004321)."""

import pathlib
import struct
import sys
import types
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient import textlist  # noqa: E402
from qqtclient.net import chat  # noqa: E402
from qqtclient.runtime import UIRuntime  # noqa: E402
from qqtclient.widgets import build  # noqa: E402


def kinds(elements):
    return [(element.kind, element.value) if element.kind != textlist.CHAR else element.text for element in elements]


class ParseTest(unittest.TestCase):
    def test_lobby_line_tags(self):
        elements = textlist.parse("/<kin=-1>/<level=120>/<name=4:uin=1000012>糖一说:hi")
        self.assertEqual(kinds(elements), [("kin", -1), ("level", 120), ("name", 1000012), "糖", "一", "说", ":", "h", "i"])
        self.assertEqual(elements[2].length, 4)

    def test_name_bytes_are_not_parsed(self):
        # A "/" inside the name stays text even when it is a face code.
        elements = textlist.parse("/<name=3:uin=7>a/mb/m", ["/m"])
        self.assertEqual(kinds(elements), [("name", 7), "a", "/", "m", "b", ("face", 0)])

    def test_links_and_disabled_tags(self):
        elements = textlist.parse("/<H>www.qq.com/<~h>/<c=FF0000>x")
        self.assertEqual(kinds(elements)[0], ("link", 1))
        self.assertEqual(kinds(elements)[11], ("link", 0))
        # Colours are not enabled in a TEXTLIST (line flag 0x10), so they stay text.
        self.assertEqual("".join(e.text for e in elements[12:]), "/<c=FF0000>x")

    def test_slash_after_slash_is_not_a_tag(self):
        self.assertEqual(kinds(textlist.parse("//<H>")), ["/", "/", "<", "H", ">"])

    def test_bad_tags_stay_text(self):
        self.assertEqual("".join(e.text for e in textlist.parse("/<name=3:uin=x>abc")), "/<name=3:uin=x>abc")
        self.assertEqual(kinds(textlist.parse("/<kin=5")), ["/", "<", "k", "i", "n", "=", "5"])


class LayoutTest(unittest.TestCase):
    def test_widths(self):
        elements = textlist.parse("/<kin=2>/<level=0>/<name=2:uin=1>ab糖", ["/x"])
        rows = textlist.layout(elements, 12, 0, lambda face: 20)
        self.assertEqual(rows, [[(0, 0, 20), (1, 20, 20), (2, 40, 0), (3, 40, 6), (4, 46, 6), (5, 52, 12)]])

    def test_row_breaks_before_the_overflowing_element(self):
        rows = textlist.layout(textlist.parse("abcd"), 12, 18, lambda face: 20)
        self.assertEqual([[cell[0] for cell in row] for row in rows], [[0, 1, 2], [3]])


class HitTest(unittest.TestCase):
    def rows(self, text, left=10, top=100):
        elements = textlist.parse(text)
        return [(left, top + number * 18, 18, textlist.Row(elements, row))
                for number, row in enumerate(textlist.layout(elements, 12, 300, lambda face: 20))]

    def test_name_covers_its_bytes(self):
        rows = self.rows("/<kin=-1>/<level=0>/<name=4:uin=1000012>糖一说:hi")
        row, index = textlist.element_under(rows, (10 + 40 + 13, 105))  # second name character
        marker = textlist.name_at(row.elements, index)
        self.assertEqual((textlist.name_text(row.elements, marker), row.elements[marker].value), ("糖一", 1000012))
        row, index = textlist.element_under(rows, (10 + 40 + 30, 105))  # "说"
        self.assertIsNone(textlist.name_at(row.elements, index))
        self.assertIsNone(textlist.name_at(row.elements, 0))  # the kin icon is before any name
        self.assertIsNone(textlist.element_under(rows, (200, 105)))  # past the line's end
        self.assertIsNone(textlist.element_under(rows, (60, 130)))  # below the row

    def test_links(self):
        rows = self.rows("go /<H>www.qq.com/<~H> now")
        row, index = textlist.element_under(rows, (10 + 6 * 5, 105))
        self.assertEqual(textlist.link_at(row.elements, index), "http://www.qq.com")
        row, index = textlist.element_under(rows, (10 + 6 * 15, 105))
        self.assertIsNone(textlist.link_at(row.elements, index))
        # 原版 bug，已修正: a link with the scheme does not get it twice.
        row, index = textlist.element_under(self.rows("/<h>HTTP://a.b"), (10 + 7, 105))
        self.assertEqual(textlist.link_at(row.elements, index), "HTTP://a.b")

    def test_lobby_line_from_notify(self):
        text = "糖一说:hi".encode("gbk")
        payload = struct.pack(">HHH", 1, 0xFFFF, len(text)) + text + struct.pack(">IIIIIi", 1000012, 0, 0, 150, 3, 5)
        message = chat.parse_section_message(payload)
        self.assertEqual(textlist.lobby_line(message), "/<kin=5>/<level=150>/<name=4:uin=1000012>糖一说:hi")
        message.kin_flag = -1
        self.assertTrue(textlist.lobby_line(message).startswith("/<kin=-1>"))
        nameless = chat.SectionMessage(1, "hello", "", 9, False)
        self.assertEqual(textlist.lobby_line(nameless), "/<kin=-1>/<level=0>/<name=3:uin=9>说:hello")


class RightButtonTest(unittest.TestCase):
    def setUp(self):
        calls = []
        self.calls = calls

        class Screen:
            rect = (0, 0, 800, 600)

            class children:
                class row:
                    type = "BUTTON"
                    rect = (100, 100, 200, 20)

                    def OnRBtnUp(pos):
                        calls.append(("OnRBtnUp", pos))

                    def OnRClick():
                        calls.append(("OnRClick",))

                    class children:
                        class name:
                            type = "LABEL"
                            rect = (0, 0, 50, 20)

                class chat:
                    type = "TEXTLIST"
                    rect = (0, 300, 400, 100)

                    def OnClick(this):
                        calls.append(("OnClick",))

                    def OnClickName(name, uin):
                        calls.append(("OnClickName", name, uin))

                    def OnRClickName(name, uin):
                        calls.append(("OnRClickName", name, uin))

                    def OnClickHLink(url):
                        calls.append(("OnClickHLink", url))

                    def OnDBClick(me):
                        calls.append(("OnDBClick", self.runtime.find(me).text))

        self.runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        self.runtime.root = build("UI", Screen, None)
        self.runtime._register(self.runtime.root)
        chat_list = self.runtime.find("UI.chat")
        elements = textlist.parse("/<name=4:uin=1000013>糖二说:看/<H>a.cn")
        rows = textlist.layout(elements, 12, 300, lambda face: 20)
        chat_list.text_rows = [(10, 310, 18, textlist.Row(elements, rows[0]))]

    def press(self, button, position, up=True):
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONDOWN, pos=position, button=button))
        if up:
            self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONUP, pos=position, button=button))

    def test_button_gets_rclick_on_press_and_rbtnup_on_release(self):
        self.press(3, (250, 110), up=False)
        self.assertEqual(self.calls, [("OnRClick",)])
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(250, 105), button=3))
        self.assertEqual(self.calls[-1], ("OnRBtnUp", (250, 105)))

    def test_release_over_a_child_reaches_the_button(self):
        # The label is not Static, so it is hit; code 10 goes up to the button.
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(110, 105), button=3))
        self.assertEqual(self.calls, [("OnRBtnUp", (110, 105))])

    def test_textlist_name_clicks(self):
        self.press(1, (15, 315))
        self.assertEqual(self.calls, [("OnClickName", "糖二", 1000013), ("OnClick",)])
        self.calls.clear()
        self.press(3, (15, 315))
        self.assertEqual(self.calls, [("OnRClickName", "糖二", 1000013)])

    def test_textlist_other_clicks(self):
        self.press(1, (10 + 24 + 3, 315))  # "说": plain text, nothing at all
        self.assertEqual(self.calls, [])
        self.press(1, (10 + 24 + 18 + 12 + 3, 315))  # the link
        self.assertEqual(self.calls, [("OnClickHLink", "http://a.cn")])
        self.calls.clear()
        self.press(1, (390, 390))  # off every element
        self.assertEqual(self.calls, [("OnClick",)])
        self.calls.clear()
        self.press(3, (390, 390))  # no OnRClick for a TEXTLIST
        self.assertEqual(self.calls, [])

    def test_textlist_double_click_on_a_name_stores_the_uin(self):
        self.press(1, (15, 315))
        self.press(1, (15, 315))
        self.assertEqual(self.calls[-1], ("OnDBClick", "1000013"))


if __name__ == "__main__":
    unittest.main()
