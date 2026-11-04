"""Execute native result portrait position, pose guards and part animation.

The raw graphics avatar constructor supplies one controlled part with asset
frame counts. Portrait/controller setters and raw avatar/part update methods
run unchanged. This does not emulate D3D rasterization or worn equipment.
"""

import argparse
import configparser
import json
import pathlib
import struct
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from qqtclient.dimg import parse
from qqtclient.resources import Resources
from win32emu import Win32Emu
from unicorn.x86_const import UC_X86_REG_ECX


def run(root):
    e = Win32Emu(root / "Client.exe", root)
    resources = Resources(root)
    e.handlers["__dllonexit"] = (lambda e: e.arg(0)), 0
    prototype = e.malloc(0x48)
    e.call(0x487360, ecx=prototype)
    template = e.read(prototype, 0x48)
    counts, avatars = {}, {}

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b"\xe9" + struct.pack("<i", stub - address - 5))

    def raw_avatar(e):
        avatar = e.uc.reg_read(UC_X86_REG_ECX)
        role = e.arg(0)
        e.w32(avatar, 0x797020)
        e.w32(avatar + 0x25C, role)
        e.w32(avatar + 0x268, 1)
        e.w32(avatar + 0x26C, 270)
        shadow = e.make_object(f"shadow{avatar}", 4, size=0x80)
        e.handlers[f"shadow{avatar}+0x4"] = (lambda e: 0), 1
        e.w32(avatar + 0x320, shadow)
        part = e.malloc(0x98)
        e.w32(part, 0x797058)
        e.w32(part + 0x14, 1)
        e.w32(part + 0x1C, 270)
        e.w32(part + 0x34, 0xFFFFFFFF)
        parser = configparser.ConfigParser()
        raw = resources.read(f"object/player/player{role}.ini")
        parser.read_string(raw.decode("gbk") if raw else "")
        cloth = parser.getint("stand", "cloth", fallback=0)
        for action, name in ((1, "stand"), (8, "lose"), (10, "win")):
            data = e.malloc(0x48)
            e.write(data, template)
            e.w32(data + 0x18, 200)
            resource = resources.read(f"object/cloth/cloth{cloth}_{name}.img")
            sprite = parse(resource) if resource else None
            counts[data] = len(sprite.frames) // sprite.directions if sprite else 1
            e.w32(part + 0x3C + 4 * action, data)
        e.w32(avatar + 0x278, part)
        avatars[avatar] = part
        return avatar

    patch(0x424460, "raw_avatar", raw_avatar, 2)
    patch(0x427250, "frame_count", lambda e: counts[e.uc.reg_read(UC_X86_REG_ECX)], 0)
    path = e.malloc(512)
    for role in (1, 2, 18, 31, 32, 41):
        parser = configparser.ConfigParser()
        raw = resources.read(f"object/player/player{role}.ini")
        parser.read_string(raw.decode("gbk") if raw else "")
        print(json.dumps(dict(kind="config", role=role, ini=raw.decode("gbk") if raw else "")))
        # Populate the original role-style table from its own INI values,
        # then execute the native action/part selectors and filename builder.
        style = e.malloc(0x25C)
        for slot in range(19):
            name = e.cstr(e.call(0x448930, (slot,))).decode("ascii")
            for index in range(7):
                action = e.r32(0x7DB13C + index * 4)
                section = e.cstr(e.call(0x4476D0, (action,))).decode("ascii")
                e.w32(style + 8 + slot * 28 + index * 4, parser.getint(section, name, fallback=0))
        for outcome in (0, 1, 2):
            portrait = e.malloc(0x34)
            e.call(0x5DCF44, (role, 1), ecx=portrait)
            if e.call(0x5DD02A, ecx=portrait) & 255 != 1:
                raise RuntimeError("portrait initialization failed")
            e.call(0x5DD1C3, (100, 140), ecx=portrait)
            e.call(0x5DD38D if outcome == 0 else 0x5DD365, ecx=portrait)
            avatar = e.r32(portrait + 0x2C)
            part = avatars[avatar]
            action = e.r32(avatar + 0x268)
            parts = []
            for slot in range(19):
                present = e.call(0x453A60, (slot, action), ecx=style)
                if not present:
                    continue
                base = e.call(0x453A60, (slot, 1), ecx=style)
                identifier = e.call(0x4269F0, (slot, base, action))
                e.call(0x61FD25, (identifier, path))
                name = e.cstr(path).decode("ascii")
                parts.append(dict(slot=slot, configured=present, base=base, path=name,
                                  exists=resources.read(name) is not None))
            elapsed = 0
            for dt in (0, 199, 1, 200, 600, 17, 983, 4000):
                if dt:
                    e.call(0x5DD161, (dt,), ecx=portrait)
                elapsed += dt
                print(json.dumps(dict(kind="pose", role=role, outcome=outcome, action=action, elapsed=elapsed, dt=dt,
                                      position=struct.unpack("<ii", e.read(part + 0x7C, 8)),
                                      shadow_position=struct.unpack("<ii", e.read(e.r32(avatar + 0x320) + 8, 8)),
                                      frame_count=counts[e.r32(part + 0x3C + action * 4)],
                                      frame=e.r32(part + 0x28), remainder=e.r32(part + 0x2C),
                                      parts=parts)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("client_root", type=pathlib.Path)
    run(parser.parse_args().client_root)
