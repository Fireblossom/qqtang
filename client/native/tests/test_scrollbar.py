"""SCROLLBAR / SPIN / TEXTLIST scrolling replayed from gameUI.dll (docs/scrollbar-re.md)."""

from __future__ import annotations

import pathlib
import sys
import types
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

import pygame  # noqa: E402

from qqtclient import scrollbar  # noqa: E402
from qqtclient.natives import build_natives  # noqa: E402
from qqtclient.render import Renderer  # noqa: E402
from qqtclient.runtime import UIRuntime  # noqa: E402
from qqtclient.widgets import build  # noqa: E402

WGTSTYLE_VERTICAL, WGTSTYLE_PRIMARY = 8, 16


class TVScroll:
    """uiTemplate.TVScroll."""
    type = "SCROLLBAR"
    style = WGTSTYLE_VERTICAL
    extendstyle = 2


class ScrollRuntimeTest(unittest.TestCase):
    def setUp(self) -> None:
        events = self.events = []
        bars = []

        class UI:
            rect = (0, 0, 800, 600)

            class children:
                class dlg:
                    rect = (100, 100, 300, 300)

                    class children:
                        # The room's map list: top-down (extendstyle 2), pagesize -5.
                        class scroll(TVScroll):
                            rect = (138, 42, 26, 428)
                            pos = 0
                            pagesize = -5

                            def OnPosChange():
                                events.append(("pos", bars[0].scroll_pos))

                            class children:
                                class blockbtn:
                                    type = "BUTTON"
                                    moveable = 1
                                    rect = (0, 0, 26, 42)

                                    def OnClick(this):
                                        events.append(("block click", this))

                                class spinup:
                                    type = "SPIN"
                                    rect = (5, -17, 18, 18)

                                    def OnClick(this):
                                        events.append(("up", this))

                                class spindown:
                                    type = "SPIN"
                                    style = WGTSTYLE_PRIMARY
                                    rect = (5, 430, 18, 18)

                                    def OnClick(this):
                                        events.append(("down", this))

                        # A bar without a thumb never reports (uiSelRoom mapFilterDlg).
                        class bare(TVScroll):
                            rect = (0, 0, 16, 73)

                            def OnPosChange():
                                events.append(("bare", 0))

        self.runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        self.runtime.root = build("UI", UI, None)
        self.runtime._register(self.runtime.root)
        self.natives = build_natives(self.runtime, types.SimpleNamespace(root=pathlib.Path("/nonexistent")), types.SimpleNamespace())
        self.bar = self.runtime.find("UI.dlg.scroll")
        self.thumb = self.runtime.find("UI.dlg.scroll.blockbtn")
        bars.append(self.bar)

    def press(self, x: int, y: int) -> None:
        self.runtime.handle(pygame.event.Event(pygame.MOUSEMOTION, pos=(x, y), rel=(0, 0), buttons=(0, 0, 0)))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONDOWN, pos=(x, y), button=1))

    def release(self, x: int, y: int) -> None:
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(x, y), button=1))
        self.runtime.last_press = (0.0, (0, 0), None)  # no double clicks between steps

    def test_constructor_defaults_and_attributes(self) -> None:
        self.assertEqual((self.bar.scroll_min, self.bar.scroll_max, self.bar.page_size), (0, 10, -5))
        bare = self.runtime.find("UI.dlg.bare")
        self.assertEqual((bare.scroll_pos, bare.page_size, bare.scroll_extend), (0, 2, 2))

    def test_set_pos_clamps_places_thumb_and_fires_on_pos_change(self) -> None:
        self.natives["Win_SetRange"]("UI.dlg.scroll", 20)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 10)
        # Track 428 - 42 = 386 px over 20 steps, top-down.
        self.assertEqual((self.thumb.y, self.events), (193, [("pos", 10)]))
        self.natives["Win_SetScrollPos"]("UI.dlg.scroll", 99)
        self.assertEqual((self.natives["Win_GetPos"]("UI.dlg.scroll"), self.thumb.y), (20, 386))
        self.natives["Win_SetPos"]("UI.dlg.scroll", 20)  # unchanged still reports
        self.assertEqual(self.events[-2:], [("pos", 20), ("pos", 20)])
        self.natives["Win_SetRange"]("UI.dlg.scroll", -3)  # [0, 0]; the position stays
        self.assertEqual(self.natives["Win_GetScrollPos"]("UI.dlg.scroll"), 20)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 5)
        self.assertEqual((self.bar.scroll_pos, self.thumb.y), (0, 0))

    def test_bar_without_thumb_and_other_widgets_ignore_positions(self) -> None:
        self.natives["Win_SetPos"]("UI.dlg.bare", 3)
        self.assertEqual((self.natives["Win_GetPos"]("UI.dlg.bare"), self.events), (3, []))
        self.natives["Win_SetPos"]("UI.dlg", 3)
        self.assertEqual(self.natives["Win_GetPos"]("UI.dlg"), 0)

    def test_bottom_up_bar_places_thumb_from_the_bottom(self) -> None:
        self.bar.scroll_extend = 0  # chat bars
        self.natives["Win_SetRange"]("UI.dlg.scroll", 4)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 1)
        self.assertEqual(self.thumb.y, 386 - 96)
        self.natives["Win_SetRange"]("UI.dlg.scroll", 0)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 0)
        self.assertEqual(self.thumb.y, 386)

    def test_track_press_pages_by_pagesize_and_repeats_after_delay(self) -> None:
        self.natives["Win_SetRange"]("UI.dlg.scroll", 20)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 10)
        self.events.clear()
        # Below the thumb (at y 335..377) subtracts pagesize -5.
        self.press(251, 560)
        self.assertEqual(self.bar.scroll_pos, 15)
        self.runtime.tick(300)
        self.assertEqual(self.bar.scroll_pos, 15)
        self.runtime.tick(150)  # past 0.4 s: a step every frame
        self.assertEqual(self.bar.scroll_pos, 20)
        self.runtime.tick(16)  # the thumb (528..570) reached the mouse
        self.assertEqual(self.bar.scroll_pos, 20)
        self.release(251, 560)
        self.runtime.tick(1000)
        # Above the thumb adds -5.
        self.press(251, 150)
        self.assertEqual(self.bar.scroll_pos, 15)
        self.release(251, 150)
        self.assertEqual(self.events[:2], [("pos", 15), ("pos", 20)])

    def test_dragging_the_thumb_reads_the_position_back_and_snaps(self) -> None:
        self.natives["Win_SetRange"]("UI.dlg.scroll", 10)
        self.natives["Win_SetPos"]("UI.dlg.scroll", 0)
        self.events.clear()
        self.press(250, 150)  # on the thumb, 8 px below its top
        self.runtime.handle(pygame.event.Event(pygame.MOUSEMOTION, pos=(260, 150 + 200), rel=(0, 0), buttons=(1, 0, 0)))
        # 200 * 10 // 386 = 5, and the thumb snaps to 5 * 386 // 10.
        self.assertEqual((self.bar.scroll_pos, self.thumb.x, self.thumb.y), (5, 0, 193))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEMOTION, pos=(250, 2000), rel=(0, 0), buttons=(1, 0, 0)))
        self.assertEqual((self.bar.scroll_pos, self.thumb.y), (10, 386))
        self.release(250, 2000)
        self.assertNotIn("block click", [name for name, _ in self.events])
        self.assertIsNone(self.runtime.moving)

    def test_spins_step_by_one_inside_the_range(self) -> None:
        self.natives["Win_SetRange"]("UI.dlg.scroll", 1)
        self.press(250, 580)  # spindown at bar y 430
        self.release(250, 580)
        self.assertEqual(self.bar.scroll_pos, 1)
        self.press(250, 580)
        self.release(250, 580)
        self.assertEqual(self.bar.scroll_pos, 1)  # out of range: no step, no OnPosChange
        self.press(250, 130)  # spinup at bar y -17
        self.release(250, 130)
        self.assertEqual(self.bar.scroll_pos, 0)
        self.assertEqual([name for name, _ in self.events], ["down", "pos", "down", "up", "pos"])

    def test_holding_a_spin_repeats_its_click(self) -> None:
        self.natives["Win_SetRange"]("UI.dlg.scroll", 9)
        self.press(250, 580)
        self.runtime.tick(410)
        self.runtime.tick(16)
        self.assertEqual(self.bar.scroll_pos, 2)
        self.assertEqual([name for name, _ in self.events], ["down", "pos", "down", "pos"])


class TextListScrollTest(unittest.TestCase):
    def setUp(self) -> None:
        class UI:
            class children:
                class chatList:
                    type = "TEXTLIST"
                    rect = (3, 20, 320, 98)
                    textsize = 12
                    rowspace = 6
                    scrollspace = 18

                    class children:
                        class chatScroll(TVScroll):
                            extendstyle = 0
                            rect = (326, 0, 14, 90)
                            pagesize = 8

                            class children:
                                class blockbtn:
                                    type = "BUTTON"
                                    moveable = 1
                                    rect = (0, 10, 14, 23)

                class chatEdit:
                    type = "RICHEDIT"
                    rect = (546, 500, 234, 20)

        self.runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        self.runtime.root = build("UI", UI, None)
        self.runtime._register(self.runtime.root)
        self.natives = build_natives(self.runtime, types.SimpleNamespace(root=pathlib.Path("/nonexistent")), types.SimpleNamespace())
        self.list = self.runtime.find("UI.chatList")
        self.bar = self.runtime.find("UI.chatList.chatScroll")
        self.thumb = self.runtime.find("UI.chatList.chatScroll.blockbtn")
        renderer = object.__new__(Renderer)
        renderer.face_codes = []
        renderer.ui_call = self.runtime.call
        self.renderer = renderer

    def rows(self):
        return [(text, y) for text, _, y in self.renderer.list_rows(self.list)]

    def add(self, count: int) -> None:
        for index in range(count):
            self.natives["Win_SetText"]("UI.chatList", f"line{index}", 207)

    def test_short_text_sits_at_the_bottom_and_disables_the_bar(self) -> None:
        self.add(2)
        # total = 6 + (12 + 6) * 2 = 42; drawn from 98 - 42.
        self.assertEqual(self.rows(), [("line0", 62), ("line1", 80)])
        self.assertFalse(self.bar.enabled)
        self.assertEqual((self.bar.scroll_max, self.thumb.y), (0, 90 - 23))

    def test_bar_and_wheel_scroll_back_in_pixels(self) -> None:
        self.add(10)  # total 186, 88 px hidden: range ceil(88 / 18) = 5
        self.assertEqual(self.rows()[-1], ("line9", 80))
        self.assertTrue(self.bar.enabled)
        self.assertEqual((self.bar.scroll_max, self.bar.scroll_pos, self.thumb.y), (5, 0, 67))
        # The bar runs bottom-up: a press above the thumb adds pagesize 8,
        # the list scrolls 18 px a step and clamps at 88.
        self.runtime.handle(pygame.event.Event(pygame.MOUSEMOTION, pos=(335, 30), rel=(0, 0), buttons=(0, 0, 0)))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONDOWN, pos=(335, 30), button=1))
        self.assertEqual((self.list.list_offset, self.bar.scroll_pos, self.thumb.y), (88, 5, 0))
        self.assertEqual(self.rows()[0], ("line0", 6))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(335, 30), button=1))
        # The bar is focused now, so the wheel reaches the list: 120 px a notch down.
        self.runtime.handle(pygame.event.Event(pygame.MOUSEWHEEL, x=0, y=-1, flipped=False))
        self.assertEqual((self.list.list_offset, self.bar.scroll_pos), (0, 0))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEWHEEL, x=0, y=1, flipped=False))
        self.assertEqual(self.list.list_offset, 88)

    def test_wheel_follows_focus_not_the_mouse(self) -> None:
        self.add(10)
        self.rows()
        self.runtime.set_focus(self.runtime.find("UI.chatEdit"))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEMOTION, pos=(50, 50), rel=(0, 0), buttons=(0, 0, 0)))
        self.runtime.handle(pygame.event.Event(pygame.MOUSEWHEEL, x=0, y=1, flipped=False))
        self.assertEqual(self.list.list_offset, 0)
        self.runtime.focus = None
        self.runtime.handle(pygame.event.Event(pygame.MOUSEWHEEL, x=0, y=1, flipped=False))
        self.assertEqual(self.list.list_offset, 88)

    def test_new_lines_keep_the_offset_from_the_bottom(self) -> None:
        self.add(10)
        self.rows()
        scrollbar.list_scroll_to(self.list, 36, self.runtime.call)
        self.assertEqual(self.bar.scroll_pos, 2)
        self.add(1)
        # The view keeps its distance from the bottom, so it moves up a line.
        self.assertEqual(self.rows()[-1], ("line8", 80))
        self.assertEqual((self.list.list_offset, self.bar.scroll_max), (36, 6))


if __name__ == "__main__":
    unittest.main()
