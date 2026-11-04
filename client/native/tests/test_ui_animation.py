"""Replay original gameUI clock/state transitions and actual image overflow."""

import dataclasses
import json
import pathlib
import sys
import types
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from qqtclient.render import Renderer
from qqtclient.natives import build_natives
from qqtclient.runtime import UIRuntime
from qqtclient.widgets import Widget


class UIAnimationNativeTest(unittest.TestCase):
    def test_native_sequences_through_runtime_and_renderer(self):
        records = [json.loads(line) for line in (ROOT / 'tests/fixtures/ui_animation_v848.jsonl').read_text().splitlines()]
        total = 0
        for row in records:
            if row['kind'] == 'image_change':
                widget = Widget('UI.animation', type('ImageWidget', (), {'bkimage': 'frames.img'}), None, 0)
                for key, value in row['before'].items():
                    setattr(widget.animation, key, value)
                runtime = UIRuntime(types.SimpleNamespace(namespace={}))
                runtime._register(widget)
                natives = build_natives(runtime, types.SimpleNamespace(root=pathlib.Path('/nonexistent')), types.SimpleNamespace())
                natives['Win_SetImg'](widget.path, row['image'])
                self.assertEqual(dataclasses.asdict(widget.animation), row['after'])
                continue
            attrs = {'type': row['type']}
            if row['kind'] == 'sequence':
                attrs.update(framescheme=row['scheme'], framespeed=row['speed'], loopspeed=row['loop_speed'],
                             bkimage='frames.img', bkImgFlag=row['image_flag'])
            widget = Widget('UI.animation', type('AnimationWidget', (), attrs), None, 0)
            if row['kind'] == 'defaults':
                self.assertEqual(widget.frame_speed, row['speed'])
                self.assertEqual(widget.loop_speed, row['loop_speed'])
                self.assertEqual(dataclasses.asdict(widget.animation), row['initial'])
                for index, expected in enumerate(row['rows']):
                    actual = [value for state in range(4) for value in widget.frame_bounds(state, index)]
                    self.assertEqual(actual, expected)
                continue
            runtime = UIRuntime(types.SimpleNamespace(namespace={}))
            runtime.root = widget
            runtime._register(widget)
            pictures = [types.SimpleNamespace(index=i, set_alpha=lambda value: None) for i in range(row['count'])]
            draws = []
            renderer = object.__new__(Renderer)
            renderer.sprites = types.SimpleNamespace(get=lambda path: [(p, 0, 0) for p in pictures])
            renderer.screen = types.SimpleNamespace(blit=lambda picture, *args: draws.append(picture.index))
            for frame, expected in enumerate(row['samples']):
                with self.subTest(type=row['type'], scheme=row['scheme'], speed=row['speed'],
                                  loop=row['loop_speed'], flag=row['image_flag'], frame=frame):
                    flags = expected['flags']
                    widget.enabled, widget.pressed, widget.hover = not (flags & 8), bool(flags & 2), bool(flags & 1)
                    widget.checked = int(bool(flags & 16))
                    runtime.tick(expected['dt'] * 1000)
                    self.assertEqual(dataclasses.asdict(widget.animation), expected['before'])
                    draws.clear()
                    renderer._draw_image(widget, 0, 0, 1)
                    self.assertEqual(draws, expected['draws'])
                    self.assertEqual(dataclasses.asdict(widget.animation), expected['after'])
                    total += 1
        self.assertEqual((len(records), total), (293, 13824))


if __name__ == '__main__':
    unittest.main()
