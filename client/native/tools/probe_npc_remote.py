"""Execute default NPC remote construction, record consumption and held frames.

005D086B/005D1348 and 005ABE96 -> 005D09D6 -> 005D13C2 execute. Input
is one decoded movement record with no corner and a distant endpoint; the
pending-record queue is empty. Graphics, NPC classification and ownership
are controlled, and this does not reproduce the full network queue policy.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_FPCW
from win32emu import Win32Emu


def create_remote_actor(root):
    e = Win32Emu(root / 'Client.exe', root)
    e.uc.reg_write(UC_X86_REG_FPCW, 0x007F)
    e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0
    scene, actor, human, arbitrator, rule, controller = [e.malloc(n) for n in (0x6000, 0x1000, 32, 32, 32, 32)]
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    e.w32(0x806A78+0x7D4, scene)
    e.w32(scene+0x37F8, scene)
    e.w32(scene+0x4EA4, rule)
    e.w32(scene+0x3824, human)
    e.w32(scene+0x3830, arbitrator)
    e.w32(actor, 0x7A1D2C)
    e.w32(actor+0x3FC, scene)
    e.w32(actor+0x36C, controller)
    e.w32(actor+0x3CC, -5001)
    attrs = e.make_object('attrs', 48, size=0x400)
    e.w32(actor+0x400, attrs)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9'+struct.pack('<i', stub-address-5))

    patch(0x534AD0, 'is_npc', lambda e: 1, 1)
    patch(0x534990, 'is_local', lambda e: 0, 1)
    bridge = e.malloc(64)
    e.write(bridge, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    e.write(0x6200CE, b'\xe9'+struct.pack('<i', bridge-0x6200CE-5))
    return e, scene, actor, rule, controller


def run(root):
    e, scene, actor, rule, controller = create_remote_actor(root)
    rates = list(range(11))+[11, 255]
    output = e.malloc(4)

    def scalar(function, rate):
        wrapper = e.malloc(32)
        code = b'\x68'+struct.pack('<I', rate)
        code += b'\xe8'+struct.pack('<i', function-(wrapper+10))
        code += b'\x83\xc4\x04\xd9\x1d'+struct.pack('<I', output)+b'\xc3'
        e.write(wrapper, code)
        e.call(wrapper)
        return struct.unpack('<f', e.read(output, 4))[0]

    for rate in rates:
        print(json.dumps(dict(kind='scalar', rate=rate, remote=scalar(0x5C450F, rate),
                              owner=scalar(0x5C452B, rate))))
    for role in (25, 27, 32):
        e.write(actor+0x328, struct.pack('<H', 30001 if role == 32 else 20001))
        e.write(actor+0x394, bytes([role, 7]))
        e.w32(rule+8, 1 if role == 32 else 8)
        for rate in rates:
            for direction, end in enumerate(((580, 260), (300, 20), (20, 260), (300, 500))):
                for dt in (1, 17, 20, 25, 100):
                    e.write(actor+0x338, struct.pack('<ff', 300, 260))
                    e.write(actor+0x3B7, b'\x01\x01')
                    e.call(0x5C06C9, (actor,), ecx=controller)
                    remote, message = e.malloc(0x80), e.malloc(0x40)
                    e.call(0x5D086B, (actor, scene), ecx=remote)
                    e.w32(actor+0x47C, remote)
                    e.w32(message+8, 1)
                    e.write(message+0x10, bytes([direction, rate]))
                    e.w32(message+0x14, 300)
                    e.w32(message+0x18, 260)
                    e.w32(message+0x2C, end[0])
                    e.w32(message+0x30, end[1])
                    e.call(0x5D1348, (message,), ecx=remote)
                    speed = struct.unpack('<f', e.read(actor+0x3B0, 4))[0]
                    e.call(0x5ABE96, (dt,), ecx=actor)
                    print(json.dumps(dict(kind='held', role=role, rate=rate, direction=direction,
                                          dt=dt, start=[300, 260], end=end, scalar=speed,
                                          position=struct.unpack('<ff', e.read(actor+0x338, 8)))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
