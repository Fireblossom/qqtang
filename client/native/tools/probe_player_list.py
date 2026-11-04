"""Observe v848 player-row defaults, resource filenames and renderer colors.

Usage: .venv/bin/python tools/probe_player_list.py CLIENT_ROOT
Runs actual row construction/init/update/draw with controlled actor/portrait
and text APIs, then actual Client.exe filename generation, gameUI default
background-color initialization and GFX Direct3D clear dispatch.
"""

import argparse
import json
import pathlib
import struct

from probe_sculpture_animation import picture_fixture
from qqtclient import dimg
from qqtclient.resources import Resources
from win32emu import Win32Emu


def run(root):
    e, pictures, draws, patch = picture_fixture(root)
    resources = Resources(root)
    actor = e.make_object("actor", 16, size=0x800)
    points = 0
    e.handlers["actor+0x34"] = (lambda e: points), 0
    portrait = e.make_object("portrait", 16)
    events = []
    e.handlers["portrait+0xc"] = (lambda e: events.append(["draw_portrait"])), 0
    e.handlers["portrait+0x18"] = (lambda e: events.append(["update_portrait", e.arg(0)])), 1
    patch(0x5CB087, "resource_registry", lambda e: 0, 0)
    patch(0x5CB0E9, "portrait_factory", lambda e: portrait, 2)
    patch(0x5DD1C3, "portrait_position", lambda e: events.append(["portrait_position", e.arg(0), e.arg(1)]), 2)
    patch(0x5DD22A, "portrait_facing", lambda e: events.append(["portrait_facing", e.arg(0)]), 1)
    graphics = e.make_object("graphics", 64)
    e.w32(0x7FA3C0, graphics)
    texts = []
    e.handlers["graphics+0xf0"] = (lambda e: texts.append(
        [e.arg(0), e.arg(1), e.cstr(e.arg(2)).decode("gbk"), e.arg(3), e.arg(4)])), 5
    for seat, points in ((0, 0), (3, 10000), (7, 150000)):
        e.write(actor + 0x328, struct.pack("<H", 1))
        e.write(actor + 0x380, "糖五".encode("gbk") + b"\0")
        e.write(actor + 0x394, bytes([1, 1, seat]))
        e.write(actor + 0x3A0, b"\x01")
        panel = e.malloc(0x60)
        events.clear()
        texts.clear()
        draws.clear()
        e.call(0x5E1B1B, (actor,), ecx=panel)
        ok = e.call(0x5E1C24, ecx=panel) & 255
        e.call(0x5E1DFF, (20,), ecx=panel)
        e.call(0x5E1E46, ecx=panel)
        print(json.dumps(dict(kind="default_panel", seat=seat, points=points, ok=ok,
                              background=e.r32(panel + 0xC), card=e.r32(panel + 0x58), bound=e.r32(panel + 0x5C),
                              events=events, texts=texts, draws=draws)))
    path = e.malloc(512)
    for kind, number in ((30, 2), (31, 1)):
        for action in (1, 2, 4):
            identifier = kind << 24 | number << 8 | action
            e.call(0x61FD25, (identifier, path))
            name = e.cstr(path).decode("ascii")
            print(json.dumps(dict(kind="filename", resource_id=hex(identifier), path=name, exists=resources.read(name) is not None)))
    sprite = dimg.parse(resources.read("object/ui/game/dlg_playerList.img"))
    frame = sprite.frames[0]
    samples = [list(map(int, frame.pixels[y - frame.y, x - frame.x])) for x, y in ((100, 120), (100, 171), (100, 477))]
    print(json.dumps(dict(kind="base_panel", canvas=[sprite.width, sprite.height],
                          frame_offset=[frame.x, frame.y], samples=samples)))

    ui = Win32Emu(root / "gameUI.dll", root)
    ui.w32(0x10049420, 0xDEADBEEF)
    ui.call(0x1002629C)
    print(json.dumps(dict(kind="default_widget_background", argb=ui.r32(0x10049420))))
    gfx = Win32Emu(root / "GFX.dll", root)
    context = gfx.malloc(0x24)
    device = gfx.make_object("device", 40)
    gfx.w32(context + 0x20, device)
    clears = []
    gfx.handlers["device+0x90"] = (lambda e: clears.append([e.arg(i) for i in range(1, 7)]) or 0), 7
    gfx.call(0x10004B57, ecx=context)
    print(json.dumps(dict(kind="gfx_clear", args=clears)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
