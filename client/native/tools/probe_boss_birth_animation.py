"""Run native birth action, part loading and actor-picture animation.

The raw avatar is a controlled one-part model. Actual 005ACBFC/005B4DD9,
00425F10 (including data construction and its period setter), 005B5244 and
raw avatar/part updates execute. Texture loading uses original DIMG frame
counts; resource lookup, texture upload and shadow drawing are controlled.
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
from unicorn.x86_const import UC_X86_REG_ECX
from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / 'Client.exe', root)
    resources = Resources(root)
    counts = {}
    frame_count = [0]

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9' + struct.pack('<i', stub-address-5))

    patch(0x426FC0, 'asset_identifier', lambda e: 1234, 2)

    def load_texture(e):
        counts[e.uc.reg_read(UC_X86_REG_ECX)] = frame_count[0]
        return 1

    patch(0x487AE0, 'load_texture', load_texture, 2)
    patch(0x427250, 'frame_count', lambda e: counts[e.uc.reg_read(UC_X86_REG_ECX)], 0)

    def make_actor(role):
        avatar = e.malloc(0x330)
        e.w32(avatar, 0x797020)
        e.w32(avatar+0x25C, role)
        e.w32(avatar+0x268, 1)
        e.w32(avatar+0x26C, 270)
        shadow = e.make_object(f'shadow{avatar}', 4, size=0x80)
        e.handlers[f'shadow{avatar}+0x4'] = (lambda e: 0), 1
        e.w32(avatar+0x320, shadow)
        part = e.malloc(0x98)
        e.w32(part, 0x797058)
        e.w32(part+0x14, 1)
        e.w32(part+0x1C, 270)
        e.w32(part+0x34, 0xFFFFFFFF)
        e.w32(avatar+0x278, part)
        e.call(0x425F10, (14,), ecx=part)
        actor = e.malloc(0x1000)
        e.w32(actor+0x31C, avatar)
        e.call(0x5ACBFC, ecx=actor)
        return actor, avatar, part

    for role in (24, 28, 32, 34, 35, 36):
        ini = configparser.ConfigParser()
        ini.read_string(resources.read(f'object/player/player{role}.ini').decode('gbk'))
        cloth = ini.getint('stand', 'cloth')
        path = f'object/cloth/cloth{cloth}_birth.img'
        sprite = parse(resources.read(path))
        frame_count[0] = len(sprite.frames)//sprite.directions
        for case, deltas in (('sequence', (0, 199, 1, 200, 600, 200, 200, 200, 400, 1000)),
                             ('long', (4000,))):
            actor, avatar, part = make_actor(role)
            data = e.r32(part+0x3C+14*4)
            if case == 'sequence':
                print(json.dumps(dict(kind='config', role=role, path=path, frames=frame_count[0],
                                      directions=sprite.directions, period=e.r32(data+0x18),
                                      action=e.r32(avatar+0x268), facing=e.r32(avatar+0x26C))))
            elapsed = 0
            for dt in deltas:
                if dt:
                    e.call(0x5B5244, (dt,), ecx=actor+0x318)
                elapsed += dt
                print(json.dumps(dict(kind='frame', role=role, case=case, elapsed=elapsed, dt=dt,
                                      frame=e.r32(part+0x28), remainder=e.r32(part+0x2C),
                                      loops=e.r32(part+0x34), stopped=e.read(part+0x38, 1)[0])))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
