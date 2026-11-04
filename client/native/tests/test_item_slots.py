"""Native mixed-slot order, paging and key routing."""

import pathlib
import sys
import types
import unittest

import pygame

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.battle.scene import BattleScene
from qqtclient.battle.sidecar import Sidecar


class ItemSlotsTest(unittest.TestCase):
    def scene(self, slots):
        scene = BattleScene.__new__(BattleScene)
        scene.local_id, scene.item_page, scene.over = 1, 0, False
        scene.runtime = None
        scene.state = dict(clock=30000, actors=[dict(id=1, state=0, held=[])], slots=slots)
        scene.prop_pressed, scene.use_pressed = 0, 0
        return scene

    def test_native_order_and_exhaustion_shift_numeric_keys(self):
        scene = self.scene([[20059, 9], [20020, 5], [20058, 7], [64, 1]])
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_1))
        self.assertEqual(scene.prop_pressed, 20059)
        scene.state["slots"] = [[20059, 9], [20058, 7], [64, 1]]
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_2))
        self.assertEqual(scene.prop_pressed, 20058)
        self.assertEqual(scene._slots(), [(20059, 9), (20058, 7), (64, 1)] + [(0, 0)] * 4)

    def test_tab_applies_page_offset_to_icons_and_keys(self):
        slots = [[20059, 9], [20020, 5], [20058, 7], [41, 1], [42, 1], [43, 1], [44, 1], [64, 2], [63, 1]]
        scene = self.scene(slots)
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_TAB))
        self.assertEqual(scene._slots(), [(64, 2), (63, 1)] + [(0, 0)] * 5)
        self.assertEqual(scene._smart_use(), 64)
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_1))
        self.assertEqual(scene.use_pressed, 64)
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_TAB))
        self.assertEqual(scene._slots()[0], (20059, 9))

    def test_chat_suppresses_tab_and_shorter_inventory_resets_page(self):
        scene = self.scene([[41, 1]] * 8)
        scene.runtime = types.SimpleNamespace(namespace={"Win_IsVisible": lambda _path: True})
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_TAB))
        self.assertEqual(scene.item_page, 0)
        scene.runtime.namespace["Win_IsVisible"] = lambda _path: False
        scene.handle(pygame.event.Event(pygame.KEYDOWN, key=pygame.K_TAB))
        self.assertEqual(scene.item_page, 1)
        scene.state["slots"] = [[64, 3]]
        self.assertEqual(scene._slots(), [(64, 3)] + [(0, 0)] * 6)
        self.assertEqual(scene.item_page, 0)

    def test_sidecar_receives_local_bar_with_holes(self):
        sidecar = Sidecar.__new__(Sidecar)
        sidecar._call = lambda command: command
        bar = [20059, 0, 20020, 20058, 0, 0, 0]
        command = sidecar.start_online(pathlib.Path("fixture"), b"begin", 1, 1000007, "127.0.0.1", 58000,
                                       {1: 1000007}, False, prepared_order=bar)
        self.assertEqual(command["online"]["prepared_order"], bar)
