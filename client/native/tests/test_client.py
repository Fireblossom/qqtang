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

from qqtclient import dimg  # noqa: E402
from qqtclient.avatar import tint  # noqa: E402
from qqtclient.natives import build_natives  # noqa: E402
from qqtclient.render import Renderer, cell_width  # noqa: E402
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
        self.assertEqual(button.frame_range(4), (1, 1))
        button.hover = True
        self.assertEqual(button.state(), STATE_HOVER)
        self.assertEqual(button.frame_range(4), (2, 2))
        button.pressed = True
        self.assertEqual(button.state(), STATE_PRESSED)
        self.assertEqual(button.frame_range(4), (3, 3))
        button.enabled = False
        self.assertEqual(button.state(), STATE_DISABLED)
        self.assertEqual(button.frame_range(4), (0, 0))

    def test_default_button_frames_and_hidden_states(self) -> None:
        self.assertEqual(self.make(type="BUTTON").frame_range(3), (0, 0))
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
