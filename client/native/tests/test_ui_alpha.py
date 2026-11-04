"""Original gameUI alpha state and ARGB records, without a native window."""

import json
import pathlib
import sys
import types
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

import pygame

from qqtclient.natives import build_natives
from qqtclient.render import Renderer
from qqtclient.runtime import UIRuntime
from qqtclient.ui_alpha import alpha_byte, combined_alpha
from qqtclient.widgets import Widget


class UIAlphaNativeTest(unittest.TestCase):
    def environment(self, attributes=None, parent=None):
        runtime = UIRuntime(types.SimpleNamespace(namespace={}))
        cls = type("AlphaWidget", (), attributes or {})
        widget = Widget("UI.alpha", cls, parent, 0)
        runtime.root = widget
        runtime._register(widget)
        natives = build_natives(runtime, types.SimpleNamespace(root=pathlib.Path("/nonexistent")), types.SimpleNamespace())
        return runtime, widget, natives

    def test_state_and_image_argb_match_original(self):
        rows = [json.loads(line) for line in (ROOT / "tests/fixtures/ui_alpha_v848.jsonl").read_text().splitlines()]
        runtime, widget, natives = self.environment()
        samples = 0
        for row in rows:
            if row["kind"] == "fade":
                disappeared = []
                parent = None
                if row["parent_alpha"] is not None:
                    parent = Widget("UI.parent", type("Parent", (), {"alphafactor": row["parent_alpha"]}), None, 0)
                runtime, widget, natives = self.environment({
                    "rect": (2, 80, 50, 50), "bkimage": "alpha.img",
                    "alphafactor": row["alpha"], "alphaspeed": row["speed"],
                    "minalphafactor": row["minimum"], "OnDisappear": lambda path: disappeared.append(path),
                }, parent)
                natives["Win_SetValue"](widget.path, row["direction"], 901)
                picture = pygame.Surface((55, 55), pygame.SRCALPHA)
                calls = []
                renderer = object.__new__(Renderer)
                renderer.sprites = types.SimpleNamespace(get=lambda path: [(picture, 0, 0)])
                renderer.screen = types.SimpleNamespace(blit=lambda surface, pos, *args: calls.append([pos[0], pos[1], surface.get_alpha() << 24 | 0xFFFFFF]))
                for index, expected in enumerate(row["samples"]):
                    # Base alpha advances once per widget update, even after
                    # a late frame; timed masks/life strips use their own dt.
                    runtime.tick((1, 17, 1000, 20, 0, 25, 33, 1)[index])
                    effective = combined_alpha(widget.alpha, row["parent_alpha"])
                    self.assert_state(widget, natives, expected)
                    self.assertEqual(effective, expected["effective"])
                    self.assertEqual(len(disappeared), expected["disappear"])
                    calls.clear()
                    renderer._draw_image(widget, 2, 80, effective)
                    self.assertEqual(calls, expected["draws"])
                    if calls:
                        self.assertEqual(calls[0][2] >> 24, alpha_byte(effective))
                    samples += 1
            elif row["kind"] == "setter":
                natives["Win_SetValue"](widget.path, row["value"], row["channel"])
                self.assert_state(widget, natives, row["state"])
            elif row["kind"] == "defaults":
                self.assert_state(widget, natives, row["state"])
            elif row["kind"] == "getters":
                for channel, expected in row["values"].items():
                    self.assertEqual(natives["Win_GetValue"](widget.path, int(channel)), expected)
        self.assertEqual((len(rows), samples), (154, 1152))

    def assert_state(self, widget, natives, expected):
        self.assertEqual(widget.alpha, expected["alpha"])
        self.assertEqual(widget.alpha_speed, expected["speed"])
        self.assertEqual(widget.alpha_minimum, expected["minimum"])
        self.assertEqual(widget.alpha_direction, expected["direction"])
        self.assertEqual(natives["Win_GetValue"](widget.path, 41), expected["get_speed"])
        self.assertEqual(natives["Win_GetValue"](widget.path, 42), expected["get_alpha"])

    def test_zero_and_none_declarations_keep_native_defaults(self):
        _, widget, _ = self.environment({"alphafactor": 0, "alphaspeed": 0, "minalphafactor": 0})
        self.assertEqual((widget.alpha, widget.alpha_speed, widget.alpha_minimum), (0, 0, 0))
        _, widget, _ = self.environment({"alphafactor": None, "alphaspeed": None, "minalphafactor": None})
        self.assertEqual((widget.alpha, widget.alpha_speed, widget.alpha_minimum), (1, 1, 0))

    def test_declared_parent_and_child_alpha_reach_tree_draw(self):
        class Root:
            alphafactor = 0.8

        parent = Widget("UI", Root, None, 0)
        _, widget, _ = self.environment({"alphafactor": 0.6, "bkimage": "alpha.img"}, parent)
        parent.children.append(widget)
        renderer = object.__new__(Renderer)
        renderer.screen = types.SimpleNamespace()
        renderer.draw_callbacks = {}
        calls = []
        renderer._draw_image = lambda w, x, y, alpha: calls.append((w.path, alpha_byte(alpha)))
        renderer._draw(parent, 0, 0, 1.0, None)
        self.assertEqual(calls, [(widget.path, alpha_byte(combined_alpha(0.6, 0.8)))])


if __name__ == "__main__":
    unittest.main()
