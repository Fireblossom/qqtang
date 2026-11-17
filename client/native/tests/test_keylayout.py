"""Custom keyboard layout: KEYEDIT boxes, storage and battle keys."""

import os
import pathlib
import sys
import tempfile
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient import keylayout
from qqtclient.battle.scene import BattleScene
from qqtclient.battle.sidecar import MOVE_LEFT, MOVE_UP
from qqtclient.widgets import Widget


class TKeyEdit:
    type = "KEYEDIT"
    rect = (75, 77, 43, 12)


class ScriptKeyLayout:
    pass


def key_event(key):
    return pygame.event.Event(pygame.KEYDOWN, key=key)


class KeyEditTest(unittest.TestCase):
    def test_names_follow_the_gameui_table(self):
        self.assertEqual([keylayout.key_name(vk) for vk in (0x41, 0x37, 0x20, 0x11, 0x60, 0x7B, 0xDE, 0x5B, -1)],
                         ["A", "7", "Space", "Ctrl", "Numpad0", "F12", "'", "", ""])

    def test_capture_cycle(self):
        box = Widget("UI.setupDlg.up", TKeyEdit, None, 0)
        self.assertEqual((box.key_value, box.text), (-1, "null"))
        keylayout.set_key_value(box, 0x26)
        self.assertEqual(box.text, "Up")
        keylayout.keyedit_key(box, pygame.K_w)  # not capturing: ignored
        self.assertEqual(box.key_value, 0x26)
        keylayout.keyedit_click(box)
        self.assertEqual(keylayout.keyedit_text(box), "-")
        keylayout.keyedit_tick(box, 701)
        self.assertEqual(keylayout.keyedit_text(box), "")
        keylayout.keyedit_tick(box, 401)
        self.assertEqual(keylayout.keyedit_text(box), "-")
        keylayout.keyedit_key(box, pygame.K_CAPSLOCK)  # unnamed: still capturing
        self.assertTrue(box.key_capturing)
        keylayout.keyedit_key(box, pygame.K_w)
        self.assertEqual((box.key_value, box.text, box.key_capturing), (0x57, "W", False))
        keylayout.keyedit_key(box, pygame.K_RETURN)  # Enter starts capturing
        keylayout.keyedit_key(box, pygame.K_RETURN)
        self.assertEqual((box.key_value, box.text), (0x0D, "Enter"))
        keylayout.keyedit_click(box)
        keylayout.keyedit_click(box, right=True)  # shows the mouse, keeps the key
        self.assertEqual((box.key_value, box.text, box.key_capturing), (0x0D, "Mouse R", False))
        keylayout.keyedit_click(box)
        keylayout.keyedit_key(box, pygame.K_ESCAPE)
        self.assertEqual((box.key_value, box.key_capturing), (0x0D, False))


class StorageTest(unittest.TestCase):
    def setUp(self):
        self.cwd = os.getcwd()
        self.temp = tempfile.TemporaryDirectory()
        os.chdir(self.temp.name)

    def tearDown(self):
        os.chdir(self.cwd)
        self.temp.cleanup()

    def test_restore_defaults_then_save_round_trip(self):
        target = type("keyLayout", (), {})
        natives = keylayout.build_keylayout_natives({"keyLayout": target})
        natives["sc_keyLayout_restore"](1000001)
        self.assertEqual((target.moveUp, target.moveLeft, target.putBomb, target.smartUseItem, target.shop3, target.system),
                         (0x26, 0x25, 0x20, 0x11, 0x6D, 0x7B))
        target.moveUp, target.moveLeft, target.putBomb, target.system = 0x57, 0x41, 0x4A, 0x71
        natives["sc_keyLayout_save"](1000001)
        stored = keylayout.load(1000001)
        self.assertEqual((stored[2], stored[0], stored[4], stored[23], stored[24], stored[25]), (0x57, 0x41, 0x4A, 0x71, 0x70, 0x51))
        self.assertEqual(keylayout.load(1000002), keylayout.DEFAULT_LAYOUT)

    def test_bad_file_falls_back_to_defaults(self):
        keylayout.profile_path(7).parent.mkdir()
        keylayout.profile_path(7).write_text("zz")
        self.assertEqual(keylayout.load(7), keylayout.DEFAULT_LAYOUT)


class BattleKeysTest(unittest.TestCase):
    def test_default_layout_matches_the_fixed_keys(self):
        keys = keylayout.BattleKeys.from_layout(keylayout.DEFAULT_LAYOUT)
        self.assertEqual(keys.moves[pygame.K_UP], MOVE_UP)
        self.assertEqual(keys.bomb, {pygame.K_SPACE})
        self.assertEqual(keys.smart_use, {pygame.K_LCTRL, pygame.K_RCTRL})
        self.assertEqual(keys.page, {pygame.K_TAB})
        self.assertEqual((keys.slots[pygame.K_1], keys.slots[pygame.K_7], keys.slots[pygame.K_z], keys.slots[pygame.K_m]), (0, 6, 0, 6))

    def test_custom_layout_and_unmapped_keys(self):
        layout = bytearray(keylayout.DEFAULT_LAYOUT)
        layout[0:5] = bytes((0x41, 0x44, 0x57, 0x53, 0x0D))  # A D W S, Enter
        layout[5] = 0x12  # Alt has no battle scan code
        layout[6] = 0x10  # Shift: the left one only
        layout[9] = 0xBA  # ';' has none either; Z still uses slot 0
        keys = keylayout.BattleKeys.from_layout(bytes(layout))
        self.assertEqual(keys.moves[pygame.K_a], MOVE_LEFT)
        self.assertNotIn(pygame.K_LEFT, keys.moves)
        self.assertEqual(keys.bomb, {pygame.K_KP_ENTER})
        self.assertEqual(keys.page, frozenset())
        self.assertEqual(keys.smart_use, {pygame.K_LSHIFT})
        self.assertNotIn(pygame.K_1, keys.slots)
        self.assertEqual(keys.slots[pygame.K_z], 0)

    def test_scene_uses_the_bound_keys(self):
        scene = BattleScene.__new__(BattleScene)
        scene.local_id, scene.item_page, scene.over, scene.runtime = 1, 0, False, None
        scene.held, scene.bomb_pressed = [], False
        layout = bytearray(keylayout.DEFAULT_LAYOUT)
        layout[2], layout[4] = 0x57, 0x4A  # W up, J bubble
        scene.keys = keylayout.BattleKeys.from_layout(bytes(layout))
        scene.handle(key_event(pygame.K_w))
        scene.handle(key_event(pygame.K_SPACE))
        self.assertEqual((scene.held, scene.bomb_pressed), ([MOVE_UP], False))
        scene.handle(key_event(pygame.K_j))
        self.assertTrue(scene.bomb_pressed)


if __name__ == "__main__":
    unittest.main()
