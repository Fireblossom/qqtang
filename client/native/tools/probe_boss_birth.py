"""Execute native default Boss birth setup and complete base actor frames.

Runs 005C06C9, 005ACBFC and 005ABE96, including the real controller and
005AE672/005C07D6/005AC934. NPC classification and greeting transports are
controlled; pictures/other effects are absent. Optional effect completion is
an explicit input, not a simulation of the birth graphic or a full scene.
"""

import argparse
import json
import pathlib
import struct

from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / 'Client.exe', root)
    e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0
    scene, actor, human, other, controller, rule = [e.malloc(n) for n in (0x6000, 0x1000, 32, 32, 32, 32)]
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    e.w32(0x806A78+0x7D4, scene)
    e.w32(scene+0x37F8, scene)
    e.w32(scene+0x4EA4, rule)
    e.w32(rule+8, 1)
    e.w32(actor, 0x7A1D2C)
    e.w32(actor+0x3FC, scene)
    e.w32(scene+0x3824, human)
    e.w32(actor+0x36C, controller)
    e.write(actor+0x328, struct.pack('<H', 30001))
    e.write(actor+0x3B4, b'\x03')
    attrs = e.make_object('attrs', 48, size=0x400)
    e.w32(actor+0x400, attrs)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9' + struct.pack('<i', stub-address-5))

    patch(0x534AD0, 'is_npc', lambda e: 1, 1)
    patch(0x5A2F4C, 'greeting_transport', lambda e: 0, 2)
    patch(0x5C8794, 'greeting_dispatch', lambda e: 0, 2)
    patch(0x87960B, 'greeting_notification', lambda e: 0, 0)
    for address in (0x7F2A00, 0x7F2A04, 0x7F2A10):
        e.w32(address, e.strdup(b'greeting'))
    effect = e.make_object('effect', 16, size=64)
    ready = [True]
    e.handlers['effect+0xc'] = (lambda e: int(ready[0])), 0
    e.handlers['effect+0x4'] = (lambda e: 0), 1
    for role in (24, 28, 32, 34, 35, 36):
        configurations = [('none', 20)] if role in (24, 28) else [
            (condition, dt) for condition in ('none', 'ready', 'busy', 'ready_after_3100') for dt in (17, 20)]
        for arbitrator in (False, True):
            e.w32(scene+0x3830, human if arbitrator else other)
            for condition, dt in configurations:
                e.call(0x5C06C9, (actor,), ecx=controller)
                e.write(actor+0x394, bytes([role]))
                e.write(actor+0x3B7, b'\x09\x01')
                e.w32(actor+0x3E8, 0 if condition == 'none' else effect)
                e.call(0x5ACBFC, ecx=actor)
                samples = []
                for elapsed in range(dt, 4200+dt, dt):
                    ready[0] = condition != 'busy' and (condition != 'ready_after_3100' or elapsed >= 3100)
                    e.w32(scene+0x3838, elapsed)
                    e.call(0x5ABE96, (dt,), ecx=actor)
                    greeting_timer = struct.unpack('<i', e.read(actor+0x3CC, 4))[0]
                    samples.append([elapsed, e.read(actor+0x3B7, 1)[0], e.read(actor+0x3B8, 1)[0],
                                    e.r32(controller+8), e.call(0x5C0BDA, ecx=controller), greeting_timer])
                print(json.dumps(dict(role=role, arbitrator=arbitrator, effect=condition, dt=dt, samples=samples)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
