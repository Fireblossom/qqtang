"""Run v848's nameplate VM update/draw and F8 keyboard press edges.

Usage: .venv/bin/python tools/probe_nameplate.py CLIENT_ROOT
Actor position/name/team and DirectInput state are controlled. The actual
component initializes and updates its image wrapper; graphics/text calls and
the local player's external .eff object are observed at their API boundary.
"""

import argparse
import json
import pathlib
import struct

from probe_sculpture_animation import picture_fixture


def run(root):
    e, pictures, draws, patch = picture_fixture(root)
    code = e.malloc(64)
    e.write(code, bytes.fromhex("83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 "
                               "d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3"))
    e.write(0x6200CE, b"\xe9" + struct.pack("<i", code - 0x6200CE - 5))
    patch(0x534990, "is_local", lambda e: int(e.arg(0) & 65535 == 1), 1)
    patch(0x5CB087, "resource_registry", lambda e: 0, 0)
    effects = []
    patch(0x583FF0, "effect_create", lambda e: 0x12340000, 0)
    patch(0x582B70, "effect_position", lambda e: effects.append(["position", e.arg(0), e.arg(1)]), 2)
    patch(0x582D20, "effect_update", lambda e: effects.append(["update", e.arg(0)]), 1)
    patch(0x583AC0, "effect_draw", lambda e: effects.append(["draw"]), 0)
    world, local, remote = [e.malloc(n) for n in (0x4000, 0x800, 0x800)]
    e.w32(world + 0x3828, local)
    for actor, identifier, name, team, x, y in ((local, 1, "糖五", 1, 100, 200), (remote, 2, "糖六", 2, 160, 240)):
        e.write(actor + 0x328, struct.pack("<H", identifier))
        e.write(actor + 0x380, name.encode("gbk") + b"\0")
        e.write(actor + 0x395, bytes([team]))
        e.write(actor + 0x338, struct.pack("<ff", x, y))
        e.w32(actor + 0x3FC, world)
    graphics = e.make_object("graphics", 64)
    e.w32(0x7FA3C0, graphics)
    texts = []
    e.handlers["graphics+0xf0"] = (lambda e: texts.append(
        [e.arg(0), e.arg(1), e.cstr(e.arg(2)).decode("gbk"), e.arg(3), e.arg(4)])), 5

    def draw(label, case, enabled):
        e.call(0x5D2E80, (enabled, 0))
        draws.clear()
        texts.clear()
        effects.clear()
        e.call(0x5D3017, ecx=label)
        print(json.dumps(dict(kind="draw", case=case, enabled=enabled, texts=texts, effects=effects,
                              images=[dict(number=pictures[p]["number"],
                                           position=[e.r32(pictures[p]["host"] + 8), e.r32(pictures[p]["host"] + 12)])
                                      for p in draws])))

    for actor, case in ((local, "local"), (remote, "enemy")):
        label = e.malloc(0x14)
        e.call(0x5D2E8A, (actor,), ecx=label)
        e.call(0x5D2F2B, ecx=label)
        effects.clear()
        e.call(0x5D2F96, (20,), ecx=label)
        print(json.dumps(dict(kind="update", case=case, effects=effects)))
        draw(label, case, 0)
        draw(label, case, 1)
    e.write(remote + 0x395, b"\x01")
    draw(label, "ally", 1)
    e.write(remote + 0x380, b"Bob_7\0")
    draw(label, "ascii", 1)

    keyboard = 0x806A78 + 0x5C8
    e.call(0x5A49B8, ecx=keyboard)
    device = e.make_object("keyboard", 10)
    e.w32(keyboard + 8, device)
    pressed = False

    def device_state(e):
        raw = bytearray(256)
        if pressed:
            raw[0x42] = 0x80  # DIK_F8
        e.write(e.arg(2), raw)
        return 0

    e.handlers["keyboard+0x24"] = device_state, 3
    e.handlers["GetAsyncKeyState"] = (lambda e: 0), 1
    for pressed in (False, True, True, False, True, False):
        e.call(0x5A4C52, ecx=keyboard)
        print(json.dumps(dict(kind="keyboard", pressed=pressed, flag=e.read(keyboard + 0x23, 1)[0])))
    e.write(keyboard + 0x1FD, b"\0")
    pressed = True
    e.call(0x5A4C52, ecx=keyboard)
    print(json.dumps(dict(kind="keyboard_locked", pressed=pressed, flag=e.read(keyboard + 0x23, 1)[0])))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
