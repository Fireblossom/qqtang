"""Native DYSTRIP geometry replayed on the host's pygame backend."""

import json
import pathlib
import sys
import types
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

import pygame

from qqtclient.render import Renderer
from qqtclient.widgets import Widget


class UIStripNativeTest(unittest.TestCase):
    def test_stock_strip_dispatch_and_integer_layout_match_native(self):
        rows = [json.loads(line) for line in (ROOT / "tests/fixtures/ui_strip_v848.jsonl").read_text().splitlines()]
        compared = 0
        for row in rows:
            if row["kind"] == "constructor":
                self.assertEqual(row["vtable"], {"DYSTRIP": "0x1003dfd8", "DYLIFESTRIP": "0x1003e0b8"}[row["type"]])
                widget = Widget("UI.strip", type("Strip", (), {"type": row["type"]}), None, 0)
                self.assertEqual((widget.strip_total, widget.strip_current), (row["total"], row["current"]))
                continue
            source_w, source_h = row["source"]
            source = pygame.Surface((source_w, source_h), pygame.SRCALPHA)
            for x in range(source_w):
                source.fill(((255, 0, 0), (0, 255, 0), (0, 0, 255))[x // (source_w // 3)], (x, 0, 1, source_h))
            attrs = {"type": "DYSTRIP", "rect": (*row["point"], row["width"], source_h), "bkimage": "strip.img",
                     "totalnum": row["total"], "curnum": row["current"], "alphafactor": row["alpha"]}
            widget = Widget("UI.strip", type("Strip", (), attrs), None, 0)
            renderer = object.__new__(Renderer)
            renderer.screen = pygame.Surface((200, 60), pygame.SRCALPHA)
            renderer.screen.fill((7, 13, 19, 255))
            renderer.sprites = types.SimpleNamespace(get=lambda path: [(source, 0, 0)])
            renderer.draw_callbacks = {}
            renderer._draw(widget, 0, 0, 1.0, None)
            expected = pygame.Surface((200, 60), pygame.SRCALPHA)
            expected.fill((7, 13, 19, 255))
            # Integer room attributes have exact integer three-cap rectangles.
            # Fractional D3D raster coverage is outside this pixel comparison;
            # empty/subpixel-width threshold cases still compare as no draw.
            if not float(row["current"]).is_integer() and row["draws"]:
                continue
            for draw in row["draws"]:
                src = draw["texture"] if draw["flags"] & 4 else (0, 0, source_w, source_h)
                dst = draw["window"] if draw["flags"] & 2 else (0, 0, src[2], src[3])
                picture = source.subsurface(pygame.Rect(*(int(value) for value in src)))
                picture = pygame.transform.scale(picture, (int(dst[2]), int(dst[3])))
                picture.set_alpha(draw["argb"] >> 24)
                expected.blit(picture, (draw["point"][0] + int(dst[0]), draw["point"][1] + int(dst[1])))
            self.assertEqual(pygame.image.tobytes(renderer.screen, "RGBA"), pygame.image.tobytes(expected, "RGBA"), row)
            compared += 1
        self.assertEqual((len(rows), compared), (50, 46))


if __name__ == "__main__":
    unittest.main()
