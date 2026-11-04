"""Execute rule8 NPC ice handler, protected speed getter and actor frames.

The base rate and NPC classification are controlled; graphics are absent.
Real lookup, 005FE9D2/005ACBB7, effect construction/update/deletion and
0087E6CA execute. Inputs cover default bodies with IsAvatar=0.
"""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / 'Client.exe', root)
    e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0
    scene, actor, human, rule, controller, handler, body = [e.malloc(n) for n in
                                                          (0x6000, 0x1000, 32, 32, 32, 16, 16)]
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    e.w32(0x806A78+0x7D4, scene)
    e.w32(scene+0x37F8, scene)
    e.w32(scene+0x4EA4, rule)
    e.w32(rule+8, 8)
    e.w32(actor+0x3FC, scene)
    e.w32(actor, 0x7A1D2C)
    e.w32(actor+0x36C, controller)
    e.write(actor+0x328, struct.pack('<H', 20001))
    e.write(actor+0x3B7, b'\x01\x01')
    e.w32(actor+0x3CC, -5001)
    for offset, value in ((0x3824, human), (0x3830, human), (0x3828, actor),
                          (0x4B34, human), (0x4B38, actor), (0x4B40, human)):
        e.w32(scene+offset, value)
    vector = e.malloc(4)
    e.w32(vector, actor)
    e.w32(scene+0x264C, vector)
    e.w32(scene+0x2650, vector+4)
    e.w32(handler+4, scene)
    e.write(body, struct.pack('<HI', 20001, 0))
    attrs = e.make_object('attrs', 48, size=0x400)
    e.w32(actor+0x400, attrs)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9'+struct.pack('<i', stub-address-5))

    base = [3]
    patch(0x5AA06B, 'base_rate', lambda e: base[0], 0)
    patch(0x534AD0, 'is_npc', lambda e: 1, 1)
    e.call(0x5C06C9, (actor,), ecx=controller)
    print(json.dumps(dict(kind='config', action=e.cstr(e.call(0x4476D0, (7,))).decode())))

    def snapshot():
        effect = e.r32(actor+0x3E8)
        return dict(active=bool(effect), left=e.r32(effect+12) if effect else 0,
                    rate=e.call(0x87E6CA, ecx=actor) & 255)

    for rate in (1, 3, 8):
        base[0] = rate
        for case, steps in (
            ('expiry', [('apply', 0), ('frame', 0), ('frame', 9999), ('frame', 1), ('frame', 1), ('frame', 20)]),
            ('refresh', [('apply', 0), ('frame', 5000), ('apply', 0), ('frame', 9999), ('frame', 1), ('frame', 1)]),
        ):
            e.call(0x5C06C9, (actor,), ecx=controller)
            e.w32(actor+0x3C8, 0)
            for kind, dt in steps:
                before = snapshot()
                if kind == 'apply':
                    e.call(0x5FE9D2, (0x1166, body), ecx=handler)
                else:
                    e.call(0x5ABE96, (dt,), ecx=actor)
                print(json.dumps(dict(kind=kind, case=case, base=rate, dt=dt, before=before,
                                      after=snapshot(), body_timer=e.r32(actor+0x3C8))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
