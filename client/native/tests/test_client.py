"""Tests that need neither the original client nor a display."""

from __future__ import annotations

import ast
import pathlib
import struct
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tools"))

import types  # noqa: E402

import numpy as np  # noqa: E402
import pygame  # noqa: E402

from qqtclient import dimg  # noqa: E402
from qqtclient.avatar import tint  # noqa: E402
from qqtclient.friend import build_friend_natives  # noqa: E402
from qqtclient.net import friend  # noqa: E402
from qqtclient.natives import build_natives  # noqa: E402
from qqtclient.render import Renderer, cell_width, remaining_angle_image  # noqa: E402
from qqtclient.props import place_saved, settle_storage  # noqa: E402
from qqtclient.runtime import UIRuntime  # noqa: E402
from qqtclient.script import MissingNative, MissingReport, call_script, new_namespace, py2_div, py2_len, script_key  # noqa: E402
from qqtclient.widgets import STATE_DISABLED, STATE_HOVER, STATE_NORMAL, STATE_PRESSED, Widget, build  # noqa: E402

import prepare_ui  # noqa: E402


def dimg_bytes(frames: list[tuple[int, int, int, int, int, bytes]], width: int, height: int) -> bytes:
    header = dimg.MAGIC + struct.pack("<4I", 65536, 24, len(frames), 1) + struct.pack("<2I", 0, 0) + struct.pack("<2I", width, height)
    body = b""
    for x, y, mode, frame_width, frame_height, pixels in frames:
        body += struct.pack("<IiiI", 0, x, y, mode)
        if mode:
            body += struct.pack("<3I", frame_width, frame_height, 0) + pixels
    return header + body


class DIMGTest(unittest.TestCase):
    def test_rgb565_with_alpha_plane(self) -> None:
        # Two pixels: pure red at full alpha (32), pure blue at half alpha (16).
        pixels = struct.pack("<2H", 0xF800, 0x001F) + bytes([32, 16])
        sprite = dimg.parse(dimg_bytes([(3, 4, 3, 2, 1, pixels)], 8, 8))
        frame = sprite.frames[0]
        self.assertEqual((frame.x, frame.y), (3, 4))
        self.assertEqual(frame.pixels[0, 0].tolist(), [255, 0, 0, 255])
        self.assertEqual(frame.pixels[0, 1].tolist(), [0, 0, 255, 128])

    def test_bgra_and_empty_frames(self) -> None:
        sprite = dimg.parse(dimg_bytes([(0, 0, 8, 1, 1, bytes([1, 2, 3, 4])), (0, 0, 0, 0, 0, b"")], 4, 4))
        self.assertEqual(sprite.frames[0].pixels[0, 0].tolist(), [3, 2, 1, 4])
        self.assertIsNone(sprite.frames[1].pixels)

    def test_rejects_trailing_bytes(self) -> None:
        with self.assertRaises(dimg.DIMGError):
            dimg.parse(dimg_bytes([(0, 0, 16, 1, 1, bytes(3))], 2, 2) + b"x")


class ScriptTest(unittest.TestCase):
    def test_py2_division(self) -> None:
        self.assertEqual(py2_div(7, 2), 3)
        self.assertEqual(py2_div(-7, 2), -4)
        self.assertEqual(py2_div(7.0, 2), 3.5)

    def test_script_key_ignores_case_and_extension(self) -> None:
        self.assertEqual(script_key("res/uires/uiConst.pyc"), script_key("res/uiRes/uiConst.py"))

    def test_unknown_names_become_reported_stubs(self) -> None:
        report = MissingReport()
        namespace = new_namespace(report)
        # Module-level code (LOAD_NAME) and functions (LOAD_GLOBAL) both resolve stubs.
        exec("value = SomeNative(1)\ndef f():\n    return OtherNative() + len('ab')\nresult = f()", namespace)
        self.assertEqual((namespace["value"], namespace["result"]), (0, 2))
        self.assertEqual(set(report.calls), {"SomeNative", "OtherNative"})
        self.assertIsInstance(namespace["__builtins__"]["SomeNative"], MissingNative)

    def test_script_sys_is_isolated(self) -> None:
        namespace = new_namespace(MissingReport())
        namespace["captured"] = []
        host_stdout = sys.stdout
        exec("import sys\nclass Out:\n    def write(self, s):\n        captured.append(s)\nsys.stdout = Out()\nprint('hi')", namespace)
        self.assertEqual(namespace["captured"], ["hi\n"])
        self.assertIs(sys.stdout, host_stdout)

    def test_call_script_trims_arguments(self) -> None:
        self.assertEqual(call_script(lambda: "none", "UI.path"), "none")
        self.assertEqual(call_script(lambda this: this, "UI.path"), "UI.path")


class WidgetTest(unittest.TestCase):
    def make(self, **attributes: object) -> Widget:
        return Widget("UI.test", type("test", (), attributes), None, 0)

    def test_framescheme_state_order(self) -> None:
        button = self.make(type="BUTTON", framescheme=[(0, 0, 2, 2, 3, 3, 1, 1)])
        self.assertEqual(button.frame_range(4), (3, 3))
        button.hover = True
        self.assertEqual(button.state(), STATE_HOVER)
        self.assertEqual(button.frame_range(4), (2, 2))
        button.pressed = True
        self.assertEqual(button.state(), STATE_PRESSED)
        self.assertEqual(button.frame_range(4), (0, 0))
        button.enabled = False
        self.assertEqual(button.state(), STATE_DISABLED)
        self.assertEqual(button.frame_range(4), (1, 1))

    def test_default_button_frames_and_hidden_states(self) -> None:
        self.assertEqual(self.make(type="BUTTON").frame_range(3), (2, 2))
        overlay = self.make(type="BUTTON", framescheme=[(0, 0, 0, 0, -1, -1, -1, -1)])
        self.assertEqual(overlay.state(), STATE_NORMAL)
        self.assertIsNone(overlay.frame_range(1))

    def test_checked_row(self) -> None:
        radio = self.make(type="CHECK", framescheme=[(-1, -1, -1, -1, -1, -1, -1, -1), (0, 0, 0, 0, 0, 0, 0, 0)])
        self.assertIsNone(radio.frame_range(1))
        radio.checked = 1
        self.assertEqual(radio.frame_range(1), (0, 0))

    def test_children_follow_templates_and_order(self) -> None:
        class Template:
            class children:
                class inherited:
                    rect = (1, 2, 3, 4)

        class Screen(Template):
            class children(Template.children):
                class own:
                    caption = "x"

        root = build("UI.screen", Screen, None)
        self.assertEqual([child.path for child in root.children], ["UI.screen.inherited", "UI.screen.own"])
        self.assertEqual(root.children[0].absolute(), (1, 2))


class RuntimeTest(unittest.TestCase):
    def setUp(self) -> None:
        clicks: list[str] = []

        class Screen:
            rect = (100, 50, 400, 300)

            class children:
                class button:
                    type = "BUTTON"
                    rect = (10, 10, 30, 30)

                    def OnClick(this):
                        clicks.append(this)

                class strip:
                    type = "BUTTON"
                    rect = (0, 0, 100, 100)
                    initlayer = 10

                    def OnClick(this):
                        clicks.append(this)

                class panel:
                    rect = (200, 0, 100, 100)

                    class children:
                        class label:
                            style = 32768  # wgtstyle_static
                            rect = (10, 10, 20, 20)

                        class inner:
                            type = "BUTTON"
                            rect = (50, 50, 20, 20)

        self.clicks = clicks
        self.runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        self.runtime.root = build("UI", Screen, None)
        self.runtime._register(self.runtime.root)
        self.natives = build_natives(self.runtime, types.SimpleNamespace(root=pathlib.Path("/nonexistent")), types.SimpleNamespace())

    def test_disabled_widgets_let_clicks_through(self) -> None:
        self.assertEqual(self.runtime.hit_test((120, 70)).path, "UI.strip")
        self.natives["Win_EnableWidget"]("UI.strip", 0)
        self.assertEqual(self.runtime.hit_test((120, 70)).path, "UI.button")

    def test_any_enabled_widget_takes_the_click(self) -> None:
        self.assertEqual(self.runtime.hit_test((390, 140)).path, "UI.panel")  # no handler needed
        self.assertEqual(self.runtime.hit_test((315, 65)).path, "UI.panel")  # Static label defers to its parent
        self.assertEqual(self.runtime.hit_test((355, 105)).path, "UI.panel.inner")

    def test_disabled_widget_hides_its_subtree(self) -> None:
        self.natives["Win_EnableWidget"]("UI.panel", 0)
        self.assertEqual(self.runtime.hit_test((355, 105)).path, "UI")

    def test_positions_are_screen_coordinates(self) -> None:
        self.assertEqual((self.natives["Win_GetX"]("UI.button"), self.natives["Win_GetY"]("UI.button")), (110, 60))
        self.natives["Win_Move2Pos"]("UI.button", 150, 80)
        button = self.runtime.find("UI.button")
        self.assertEqual((button.x, button.y), (50, 30))
        self.natives["Win_SetRect"]("UI.button", 34, 200, 100, 20, 10)
        self.assertEqual(self.natives["Win_GetRect"]("UI.button", 34), (200, 100, 20, 10))

    def test_direct_alpha_channel_controls_the_widget_and_returns_its_current_value(self) -> None:
        self.natives["Win_SetValue"]("UI.button", 0.25, 42)
        button = self.runtime.find("UI.button")
        self.assertEqual(button.alpha, 0.25)
        self.assertEqual(self.natives["Win_GetValue"]("UI.button", 42), 0.25)
        button.alpha = 0.1  # a running fade can change it after the setter
        self.assertEqual(self.natives["Win_GetValue"]("UI.button", 42), 0.1)

    def test_list_items(self) -> None:
        self.natives["Win_SetText"]("UI.button", "a", 207)
        self.natives["Win_SetColorText"]("UI.button", "b", 207, 1, 2, 3)
        self.assertEqual(self.runtime.find("UI.button").items, [("a", (0, 0, 0)), ("b", (1, 2, 3))])
        self.natives["Win_SetText"]("UI.button", "", 32)
        self.assertEqual(self.runtime.find("UI.button").items, [])


class TextLayoutTest(unittest.TestCase):
    """宋体 cell metrics of gameUI.dll 0x10021E86 / 0x10021C74."""

    def test_width_counts_gbk_bytes(self) -> None:
        self.assertEqual((cell_width("1000 糖块", 12), cell_width("ab", 14)), (54, 14))

    def test_wrap_breaks_on_byte_cells(self) -> None:
        self.assertEqual(Renderer._wrap("1000 糖块", 12, 48), ["1000 糖", "块"])
        self.assertEqual(Renderer._wrap("ab\ncd", 12, 0), ["ab", "cd"])
        self.assertEqual(Renderer._wrap("糖", 12, 11), [])


class DynamicMaskTest(unittest.TestCase):
    def test_cardinal_sectors_match_native_triangle_fans(self):
        image = pygame.Surface((20, 20), pygame.SRCALPHA)
        image.fill((240, 30, 20, 200))
        points = [(15, 5), (15, 15), (5, 15), (5, 5)]
        for angle, visible in ((0, [1, 1, 1, 1]), (90, [0, 1, 1, 1]),
                               (180, [0, 0, 1, 1]), (270, [0, 0, 0, 1]), (360, [0, 0, 0, 0])):
            with self.subTest(angle=angle):
                drawn = remaining_angle_image(image, angle)
                self.assertEqual([drawn.get_at(p).a for p in points], [200 * v for v in visible])
        self.assertEqual(image.get_at((15, 5)), (240, 30, 20, 200))

    def test_non_square_image_uses_normalized_angles(self):
        image = pygame.Surface((40, 20), pygame.SRCALPHA)
        image.fill((255, 255, 255, 255))
        drawn = remaining_angle_image(image, 45)
        self.assertEqual(drawn.get_at((30, 1)).a, 0)
        self.assertEqual(drawn.get_at((37, 3)).a, 255)

    def test_angle_approaches_destination_without_overshooting(self):
        runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        mask = Widget("mask", type("Mask", (), {"type": "DYMASK", "destangle": 360, "stepangle": 180}), None, 0)
        runtime.widgets = {"mask": mask}
        runtime.tick(500)
        self.assertEqual(mask.angle_cur, 90)
        runtime.tick(5000)
        self.assertEqual(mask.angle_cur, 360)
        mask.angle_dest = 0
        runtime.tick(1000)
        self.assertEqual(mask.angle_cur, 180)
        runtime.tick(5000)
        self.assertEqual(mask.angle_cur, 0)


class DialogKeyboardTest(unittest.TestCase):
    def setUp(self):
        calls = self.calls = []

        class UI:
            class children:
                class screen:
                    class children:
                        class outside:
                            type = "EDIT"

                class dialog:
                    visible = 0

                    def OnEnter(this):
                        calls.append(("enter", this))

                    def OnEscape(this):
                        calls.append(("escape", this))

                    class children:
                        class first:
                            type = "EDIT"

                        class second:
                            type = "EDIT"

                        class button:
                            type = "BUTTON"

        self.runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        self.runtime.root = build("UI", UI, None)
        self.runtime._register(self.runtime.root)
        self.runtime.screens = {"test": "UI.screen"}
        self.runtime.current_screen = "test"
        self.dialog = self.runtime.find("UI.dialog")
        self.first = self.runtime.find("UI.dialog.first")
        self.second = self.runtime.find("UI.dialog.second")

    def open_dialog(self):
        # Exact Add Friend script sequence: focus while hidden, then show
        # and focus/capture its parent through ui_setCapture.
        self.runtime.set_focus(self.first)
        self.dialog.visible = True
        self.runtime.set_focus(self.dialog)
        self.runtime.capture = self.dialog

    def test_parent_capture_keeps_explicit_edit_and_accepts_typing(self):
        self.open_dialog()
        self.runtime.handle(pygame.event.Event(pygame.TEXTINPUT, text="hello糖"))
        self.runtime.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_BACKSPACE, mod=0))
        self.assertEqual(self.first.text, "hello")
        self.assertIs(self.runtime.focus, self.first)
        self.assertIs(self.runtime.capture, self.dialog)
        self.runtime.set_focus(self.runtime.find("UI.dialog.button"))
        self.assertEqual(self.runtime.focus.path, "UI.dialog.button")

    def test_tab_stays_in_the_modal_and_enter_escape_bubble_to_it(self):
        self.open_dialog()
        for expected in (self.second, self.first):
            self.runtime.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_TAB, mod=0))
            self.assertIs(self.runtime.focus, expected)
        self.runtime.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_RETURN, mod=0))
        self.runtime.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_ESCAPE, mod=0))
        self.assertEqual(self.calls, [("enter", "UI.dialog"), ("escape", "UI.dialog")])

    def test_other_dialog_does_not_retain_an_unrelated_edit(self):
        self.runtime.set_focus(self.runtime.find("UI.screen.outside"))
        self.dialog.visible = True
        self.runtime.set_focus(self.dialog)
        self.assertIs(self.runtime.focus, self.dialog)

    def test_modal_without_enter_handler_does_not_activate_background_screen(self):
        self.open_dialog()
        self.dialog.cls = type("EmptyDialog", (), {})
        self.runtime.find("UI.screen").cls = type("Screen", (), {"OnEnter": lambda this: self.calls.append(this)})
        self.runtime.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_RETURN, mod=0))
        self.assertEqual(self.calls, [])


class PropBarTest(unittest.TestCase):
    def test_storage_partitions_by_swapping_last_matching_item(self):
        rows = [types.SimpleNamespace(m_stItem=types.SimpleNamespace(m_nItemID=i))
                for i in (24050, 30001, 20020, 20001, 30002)]
        arranged = settle_storage(rows)
        self.assertEqual([row.m_stItem.m_nItemID for row in arranged], [20001, 20020, 30001, 30002, 24050])
        self.assertEqual([row.m_stItem.m_nItemID for row in rows], [24050, 30001, 20020, 20001, 30002])
        self.assertEqual(settle_storage([]), [])

    """QQTSection.dll 0x1002B8A3: rebuilding the room prop bar at login."""

    @staticmethod
    def item(item_id: int, status: int = 1) -> types.SimpleNamespace:
        return types.SimpleNamespace(item_id=item_id, status=status)

    def test_saved_slots_are_kept(self) -> None:
        items = [self.item(20030), self.item(20020), self.item(20033, status=0), self.item(13013)]
        self.assertEqual(place_saved(items, [0, 20020, 0, 20030, 0, 0, 0]), [0, 20020, 0, 20030, 0, 0, 0])

    def test_without_a_file_props_fill_in_inventory_order(self) -> None:
        self.assertEqual(place_saved([self.item(20030), self.item(20020)], None), [20030, 20020, 0, 0, 0, 0, 0])

    def test_new_props_skip_slots_saved_for_others(self) -> None:
        # 20027 was not saved: it may not take slot 0, which is saved for 20020.
        items = [self.item(20027), self.item(20020)]
        self.assertEqual(place_saved(items, [20020, 0, 0, 0, 0, 0, 0]), [20020, 20027, 0, 0, 0, 0, 0])


class TabTest(unittest.TestCase):
    def test_tabs_in_a_group_are_exclusive(self) -> None:
        class Screen:
            rect = (0, 0, 100, 100)

            class children:
                class a:
                    type = "TABWIN"
                    groupid = 1

                class b:
                    type = "TABWIN"
                    groupid = 1

                class c:
                    type = "TABWIN"
                    groupid = 2

        runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        runtime.root = build("UI", Screen, None)
        runtime._register(runtime.root)
        a, b, c = (runtime.find("UI." + name) for name in "abc")
        c.checked = 1
        runtime.click(a)
        runtime.click(b)
        self.assertEqual((a.checked, b.checked, c.checked), (0, 1, 1))


class AvatarTest(unittest.TestCase):
    def test_tint_keeps_lightness(self) -> None:
        mask = np.array([[[100, 100, 100, 255], [200, 200, 200, 128]]], np.uint8)
        blue = tint(mask, (215.0, 1.8, 2.2))
        self.assertEqual(blue[..., 3].tolist(), [[255, 128]])
        for pixel, grey in zip(blue[0], (100, 200)):
            lightness = (int(pixel[:3].max()) + int(pixel[:3].min())) / 2
            self.assertAlmostEqual(lightness, grey, delta=1)
            self.assertGreater(pixel[2], pixel[0])  # blue hue
        grey = tint(mask, (0.0, 0.0, 0.8))
        self.assertEqual(grey[0, 0, :3].tolist(), [80, 80, 80])


class FriendHostTest(unittest.TestCase):
    def setUp(self) -> None:
        self.calls: list[tuple] = []
        self.sent: list[tuple[int, bytes]] = []
        self.boxes: list[tuple] = []
        lobby_players = [types.SimpleNamespace(m_dwPlayerUin=1000003, m_szPlayerNickname="糖三", m_dwExtItemID=[5],
                                               m_dwKinIndex=0, m_nKinNameLen=0, m_szKinName="", m_stKinFlagID=None)]
        namespace = {
            "GetPlayerCnt": lambda mode=0: len(lobby_players),
            "fetch_PlayerInfoInPlayerList": lambda index, mode=0: types.SimpleNamespace(m_nPlayerStatus=1, m_PlayerInfo=lobby_players[index]),
            "Win_SetText": lambda path, text: self.calls.append(("Win_SetText", path, text)),
        }
        for name in ("updatePlayer", "startGlint", "endGlint", "ui_msgBox", "ReceivedRequestAddFriend", "go2playerInfo", "ui_getPlayerDetail", "EnterRoom"):
            namespace[name] = lambda *args, name=name: self.calls.append((name, *args))
        runtime = types.SimpleNamespace(namespace=namespace, show_msgbox=lambda *args: self.boxes.append(args))
        self.session = types.SimpleNamespace(
            uin=1000003, nickname="糖三", profile=types.SimpleNamespace(identity=0x2000), hall=None, handlers={}, shielded=set(),
            on_entered=[], on_disconnected=[], send=lambda command, payload: self.sent.append((command, payload)) or True)
        self.natives = build_friend_natives(runtime, self.session)

    def deliver(self, command: int, payload: bytes, inner_sequence: int = 0) -> None:
        self.session.handlers[command](types.SimpleNamespace(payload=payload, inner_sequence=inner_sequence))

    def update(self, uin: int, online: bool, name: str = "") -> bytes:
        return (struct.pack(">IIIB", 1000003, uin, 100, 1) + struct.pack(">30I", 7, *([0] * 29)) + bytes([online])
                + name.encode("gbk").ljust(20, b"\0") + struct.pack(">BII", 1, 0, 0))

    def test_roster_order_follows_updates(self) -> None:
        roster = struct.pack(">HIHH", 0, 1000003, 200, 3) + struct.pack(">200I", 1000004, 1000005, 1000006, *([0] * 197))
        self.deliver(friend.FRIEND_LIST_COMMAND, roster)
        self.deliver(friend.FRIEND_UPDATE_COMMAND, self.update(1000005, True, "五"))
        self.deliver(friend.FRIEND_UPDATE_COMMAND, self.update(1000004, False, "四"))
        self.deliver(friend.FRIEND_UPDATE_COMMAND, self.update(1000009, True, "九"))  # not in the roster: ignored
        uins = [self.natives["fetch_PlayerInfoInPlayerList"](index, 1).m_PlayerInfo.m_dwPlayerUin for index in range(3)]
        self.assertEqual(uins, [1000005, 1000006, 1000004])
        first = self.natives["fetch_PlayerInfoInPlayerList"](0, 1)
        self.assertEqual((first.m_nPlayerStatus, first.m_PlayerInfo.m_szPlayerNickname, first.m_PlayerInfo.m_stGameInfo.m_dwPoint), (1, "五", 100))
        self.assertEqual((self.natives["GetPlayerCnt"](1), self.natives["GetPlayerCnt"](0), self.natives["GetValidFriendsCount"]()), (3, 1, 3))
        self.assertEqual((self.natives["getExtItem"](0, 0, 1), self.natives["getExtItem"](0, 0, 0)), (7, 5))
        self.assertEqual((self.natives["GetAttribute"](1000004, 0), self.natives["GetAttribute"](1000009, 0), self.natives["GetAttribute"](0, 2)), (1, 0, 1))
        self.assertEqual(self.calls.count(("updatePlayer",)), 3)

    def test_application_goes_through_the_envelope(self) -> None:
        word = "糖四".encode("gbk").ljust(21, b"\0") + "你好".encode("gbk")
        self.deliver(friend.ADD_FRIEND_COMMAND, struct.pack(">IIIH", 1000004, 5, 1000003, len(word)) + word)
        self.assertEqual((self.natives["GetLeaveWordCount"](), self.calls[-1]), (1, ("startGlint",)))
        self.natives["GetLeaveWord"]()
        self.assertEqual(self.calls[-4:], [("Win_SetText", "UI.friendDlg.name", "糖四"), ("Win_SetText", "UI.friendDlg.text", "你好"),
                                           ("ReceivedRequestAddFriend", 1000004), ("endGlint",)])
        self.natives["DoFriendTask"]("n", 0, 1000004, 6)
        self.assertEqual(self.sent[-1][0], friend.ANSWER_FRIEND_COMMAND)
        self.assertEqual(self.sent[-1][1][:6], struct.pack(">HI", friend.RESULT_REJECTED, 1000004))
        self.assertEqual(self.sent[-1][1][10:], struct.pack(">I", 1000003))

    def test_answers_become_messages(self) -> None:
        self.natives["DoFriendTask"]("hi", 2, 1000003, 1)  # oneself: dropped
        self.assertEqual(self.sent, [])
        self.natives["DoFriendTask"]("hi", 2, 1000004, 1)
        self.assertEqual(self.sent[-1][0], friend.ADD_FRIEND_COMMAND)
        self.deliver(friend.ANSWER_FRIEND_COMMAND, struct.pack(">HIII", 1, 1000003, 5, 1000004), inner_sequence=9)
        self.deliver(friend.ANSWER_FRIEND_COMMAND, struct.pack(">HIII", 0, 1000003, 5, 1000004))
        self.assertEqual(self.sent[-1][0], friend.FRIEND_LIST_COMMAND)
        self.natives["GetLeaveWord"]()
        self.natives["GetLeaveWord"]()
        self.assertEqual([box[0] for box in self.boxes], ["1000004玩家已经下线", "1000004[1000004]接受了您的添加好友的请求"])
        self.assertEqual([(box[2], box[4]) for box in self.boxes], [(11, -3), (3, 0)])
        self.assertIn(("ui_msgBox", 3), self.calls)

    def found(self, uin: int, status: int, section: int = 1, room: int = 0) -> bytes:
        payload = struct.pack(">HI", 0, uin) + "糖四".encode("gbk").ljust(20, b"\0")
        payload += struct.pack(">HBBIIHHH", 4, 0, 0, 0, 1, 1, section, room) + bytes(20) + bytes([status])
        payload += struct.pack(">6IHBI4I", 1, 2, 3, 0, 100, 0, 0, 1, 0, 0, 0, 0, 0) + struct.pack(">H", 0)
        return payload + struct.pack(">IH", 0, 0) + b"\xff" * 8 + struct.pack(">I", 0) + b"\x00"

    def test_shield_is_a_local_list(self) -> None:
        self.natives["DoFriendTask"]("", 0, 1000004, 5)
        self.natives["DoFriendTask"]("", 0, 1000003, 5)  # oneself: refused
        self.natives["DoFriendTask"]("", 0, 9999, 5)  # not a uin
        self.assertEqual((self.natives["GetAttribute"](1000004, 1), self.natives["GetAttribute"](1000003, 1), self.sent), (1, 0, []))
        self.natives["DoFriendTask"]("", 0, 1000004, 7)
        self.assertEqual(self.natives["GetAttribute"](1000004, 1), 0)

    def test_pursuit_is_for_friends_only(self) -> None:
        self.natives["DoFriendTask"]("", 0, 1000004, 3)  # not on the roster
        self.assertEqual(self.sent, [])

    def test_the_unused_pursuit_enters_the_friends_room(self) -> None:
        self.session.section = types.SimpleNamespace(server_id=1, id=1)
        self.natives["DoFriendTask"]("", 0, 1000004, 3)  # not a friend: nothing
        self.assertEqual(self.sent, [])
        roster = struct.pack(">HIHH", 0, 1000003, 200, 1) + struct.pack(">200I", 1000004, *([0] * 199))
        self.deliver(friend.FRIEND_LIST_COMMAND, roster)
        self.natives["DoFriendTask"]("", 0, 1000004, 3)
        self.assertEqual(self.sent[-1][0], friend.FIND_FRIEND_COMMAND)
        self.deliver(friend.FIND_FRIEND_COMMAND, self.found(1000004, friend.FIND_STATUS_IN_ROOM, room=5))
        self.assertEqual(self.calls[-1], ("EnterRoom", 5))
        self.natives["DoFriendTask"]("", 0, 1000004, 3)
        self.deliver(friend.FIND_FRIEND_COMMAND, self.found(1000004, friend.FIND_STATUS_IN_GAME, room=5))
        self.assertEqual(self.calls[-1], ("EnterRoom", 5))  # in a game: only the notice
        self.assertNotIn(("go2playerInfo", 1000004, 0), self.calls)

    def test_find_friend_fills_the_detail(self) -> None:
        self.natives["FindFriend"](9999)
        self.assertEqual(self.sent, [])
        payload = struct.pack(">HI", 0, 1000004) + "糖四".encode("gbk").ljust(20, b"\0")
        payload += struct.pack(">HBBIIHHH", 4, 0, 0, 0, 1, 1, 1, 0) + bytes(20) + b"\x00"
        payload += struct.pack(">6IHBI4I", 1, 2, 3, 0, 100, 0, 0, 1, 0, 0, 0, 0, 0) + struct.pack(">H", 0)
        payload += struct.pack(">IH", 0, 0) + b"\xff" * 8 + struct.pack(">I", 0) + b"\x00"
        self.deliver(friend.FIND_FRIEND_COMMAND, payload)
        detail = self.natives["GetPlayerDetail"]()
        # Offline and not a friend: QQTSection copies no name or location.
        self.assertEqual((detail.m_dwFriendUin, detail.m_bStatus, detail.m_szPlayerName, detail.m_szSectionName), (1000004, 0, "", ""))
        self.assertEqual((detail.m_stGameInfo.m_nWinNum, detail.m_stGameInfo.m_dwPoint, detail.m_stKinFlagID.m_iFlagID), (1, 100, -1))
        self.assertEqual(self.calls[-2:], [("go2playerInfo", 1000004, 0), ("ui_getPlayerDetail",)])
        self.assertEqual((self.natives["GetPlayerNickNamebyUin"](1000003, 0), self.natives["GetPlayerNickNamebyUin"](1000004, 0)), ("糖三", "1000004"))


class ConversionTest(unittest.TestCase):
    def convert(self, source: str) -> str:
        tree = prepare_ui.Python2Semantics().visit(ast.parse(source))
        return ast.unparse(ast.fix_missing_locations(tree))

    def test_gbk_bytes_become_text(self) -> None:
        self.assertEqual(self.convert("x = b'\\xb5\\xc7\\xc2\\xbd'"), "x = '登陆'")

    def test_division_and_cmp_sort(self) -> None:
        self.assertEqual(self.convert("y = a / b"), "y = qqt_py2_div(a, b)")
        self.assertEqual(self.convert("y /= 2"), "y = qqt_py2_div(y, 2)")
        self.assertEqual(self.convert("items.sort(compare)"), "items.sort(key=qqt_py2_cmp_key(compare))")

    def test_len_counts_gbk_bytes(self) -> None:
        self.assertEqual(self.convert("n = len(name)"), "n = qqt_py2_len(name)")
        self.assertEqual((py2_len(" 糖块"), py2_len("ab"), py2_len([1, 2])), (5, 2, 2))


if __name__ == "__main__":
    unittest.main()

class LifeStripTest(unittest.TestCase):
    def test_health_change_animates_and_zero_draws_nothing(self):
        import pygame
        from qqtclient.render import Renderer
        widget = Widget("UI.health", type("Health", (), {"type": "DYLIFESTRIP", "rect": (0, 0, 90, 5), "totalnum": 100, "curnum": 100, "destnum": 60, "stepnum": 20, "bkimage": "bar"}), None, 0)
        runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        runtime.widgets["ui.health"] = widget
        runtime.tick(1000)
        self.assertEqual(widget.strip_current, 80)
        runtime.tick(10000)
        self.assertEqual(widget.strip_current, 60)
        source = pygame.Surface((9, 5), pygame.SRCALPHA)
        source.fill((255, 0, 0, 255))
        renderer = Renderer.__new__(Renderer)
        renderer.screen = pygame.Surface((100, 10), pygame.SRCALPHA)
        renderer.sprites = types.SimpleNamespace(get=lambda path: [(source, 0, 0)])
        renderer._draw_life_strip(widget, 0, 0, 1)
        self.assertEqual(renderer.screen.get_at((53, 2)).a, 255)
        self.assertEqual(renderer.screen.get_at((54, 2)).a, 0)
        renderer.screen.fill((0, 0, 0, 0))
        widget.strip_current = 0
        renderer._draw_life_strip(widget, 0, 0, 1)
        self.assertEqual(renderer.screen.get_at((0, 0)).a, 0)
