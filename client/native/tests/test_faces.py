"""Chat face codes become icons in rich text (gameUI.dll 0x10019149)."""

import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
from qqtclient.render import Renderer


class FakeSprites:
    def __init__(self, sizes):
        self.sizes = sizes

    def canvas(self, path):
        return self.sizes.get(path, (0, 0))


class FaceTextTest(unittest.TestCase):
    def renderer(self, codes, sizes=None):
        renderer = Renderer.__new__(Renderer)
        renderer.face_codes = codes
        renderer.sprites = FakeSprites(sizes or {})
        return renderer

    def test_first_code_in_list_order_wins(self):
        renderer = self.renderer(["/m0", "/m00", "/aiq"])
        self.assertEqual(renderer.face_tokens("a/m00b/aiq/x"), ["a", 0, "0b", 2, "/x"])

    def test_small_faces_advance_twenty_pixels(self):
        renderer = self.renderer(["/a", "/b"], {"res/uiRes/face/faces/000.img": (19, 18),
                                                "res/uiRes/face/faces/001.img": (40, 38)})
        self.assertEqual(renderer.face_size(0), (20, 18))
        self.assertEqual(renderer.face_size(1), (40, 38))
        self.assertEqual(renderer.tokens_width(["ab", 0, 1], 12), 12 + 20 + 40)

    def test_wrap_keeps_faces_whole(self):
        renderer = self.renderer(["/a"], {"res/uiRes/face/faces/000.img": (19, 18)})
        self.assertEqual(renderer.wrap_faces("abcd/aef", 12, 36), [["abcd"], [0, "ef"]])
        self.assertEqual(renderer.wrap_faces("x\n/a", 12, 0), [["x"], [0]])


if __name__ == "__main__":
    unittest.main()
