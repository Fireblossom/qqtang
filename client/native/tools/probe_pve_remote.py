"""Run PVE NPC constructor/VM initialization and its remote held frame.

TerSafe boxing, name/text initialization, avatar upload and region lookup are
controlled dependencies. No lazy memory mapping. The VM must return AL=1 and
store a real 005D086B controller before 005BACC0 is used for motion samples.
"""

import argparse
import json
import pathlib
import struct

from unicorn import UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_ECX, UC_X86_REG_FPCW
from win32emu import Win32Emu


def create_pve_actor(root, initial_cell=(33,12), region_bounds=(0,0,39,39)):
    e = Win32Emu(root / 'Client.exe', root)
    e.uc.reg_write(UC_X86_REG_FPCW, 0x007F)
    e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0
    bridge = e.malloc(64)
    e.write(bridge, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    e.write(0x6200CE, b'\xe9'+struct.pack('<i', bridge-0x6200CE-5))
    for module in e.pe.DIRECTORY_ENTRY_IMPORT:
        for entry in module.imports:
            if entry.name == b'_ftol':
                e.w32(entry.address, bridge)

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9'+struct.pack('<i', stub-address-5))

    def fullpath(e):
        output, source, size = e.arg(0), e.arg(1), e.arg(2)
        output = output or e.malloc(max(size, 0x200))
        e.write(output, (e.cstr(source)+b'\0')[:size])
        return output

    e.handlers['_fullpath'] = fullpath, 0
    # Numeric values remain in the caller's XOR representation. These three
    # external protection wrappers do not participate in remote interpolation.
    for address in (0x5BDBC4, 0x5BDF40, 0x5BDFD1):
        patch(address, f'boxing{address:x}', lambda e: 0, 2)
    boxed = e.make_object('boxing_object', 32, size=32)
    for offset in range(0, 128, 4):
        e.handlers[f'boxing_object+0x{offset:x}'] = (lambda e: 0), 2
    e.handlers['boxing_object+0x0'] = (lambda e: 0), 1
    e.handlers['boxing_object+0x4'] = (lambda e: 0), 0
    patch(0x5BDB02, 'create_boxing_object', lambda e: boxed, 0)
    avatar = e.malloc(0x330)
    e.w32(avatar, 0x797020)
    e.w32(avatar+0x268, 1)
    e.w32(avatar+0x26C, 270)
    patch(0x5CB15C, 'avatar_upload', lambda e: avatar, 2)
    patch(0x5D2F2B, 'nameplate_images', lambda e: 1, 0)
    patch(0x5D31BC, 'name_text', lambda e: 1, 0)

    def bounds(e):
        e.write(e.arg(0), struct.pack('<iiii', *region_bounds))
        return e.arg(0)

    patch(0x5E8202, 'region_bounds', bounds, 2)
    scene = e.malloc(0x8000)
    world = scene+0x1310
    rule = e.malloc(32)
    e.w32(world+0x37F8, scene)
    e.w32(scene+0x4EA4, rule)
    e.w32(rule+8, 12)
    e.w32(0x85DA74, e.r32(0x85DA74) | 1)
    e.w32(0x806A78+0x7D4, scene)
    actor = e.malloc(0x1000)
    e.call(0x5BA4F2, (world, 30300, 26, 9), ecx=actor)
    entry = e.malloc(0x48)
    values = {0:30300, 4:30047, 8:1, 0x20:26, 0x24:3, 0x28:500, 0x2C:10001,
              0x30:5, 0x34:401, 0x38:2, 0x3C:initial_cell[0], 0x40:initial_cell[1], 0x44:6}
    for offset, value in values.items():
        e.w32(entry+offset, value)
    e.write(entry+0xC, b'NAME\0')
    calls = []

    def constructor_called(uc, address, size, user):
        calls.append(uc.reg_read(UC_X86_REG_ECX))

    e.uc.hook_add(UC_HOOK_CODE, constructor_called, begin=0x5D086B, end=0x5D086B)
    result = e.call(0x5BC907, (entry,), ecx=actor) & 255
    remote = e.r32(actor+0x47C)
    assert result == 1 and remote and calls == [remote]
    assert e.r32(remote+4) == actor and e.r32(remote+8) == world
    witness=dict(kind='init', result=result, actor_vtable=hex(e.r32(actor)),
                          attributes_vtable=hex(e.r32(e.r32(actor+0x400))),
                          remote_vtable=hex(e.r32(remote)), remote_ctor_calls=len(calls),
                          owner_matches=True, world_matches=True, strategy=e.r32(actor+0x8A0))
    # Motion samples exclude graphics; initialization above has already returned.
    for offset in (0x31C, 0x40C, 0x48C, 0x418):
        e.w32(actor+offset, 0)
    e.w32(actor+0x3CC, -5001)
    human, arb = e.malloc(32), e.malloc(32)
    e.w32(world+0x3824, human)
    e.w32(world+0x3830, arb)
    patch(0x534AD0, 'registered_npc', lambda e: 1, 1)
    patch(0x534990, 'remote_owned', lambda e: 0, 1)
    return e, world, actor, remote, witness, patch


def run(root):
    e, world, actor, remote, witness, patch = create_pve_actor(root)
    print(json.dumps(witness))
    for rate in list(range(11))+[11, 255]:
        for direction, end in enumerate(((580,260),(300,20),(20,260),(300,500))):
            for dt in (1,17,20,25,100):
                e.write(actor+0x338, struct.pack('<ff',300,260))
                e.write(actor+0x3B7,b'\x01\x01')
                message=e.malloc(0x40)
                e.w32(message+8,1)
                e.write(message+0x10,bytes([direction,rate]))
                for offset,value in ((0x14,300),(0x18,260),(0x2C,end[0]),(0x30,end[1])):
                    e.w32(message+offset,value)
                e.call(0x5D1348,(message,),ecx=remote)
                e.call(0x5BACC0,(dt,),ecx=actor)
                print(json.dumps(dict(kind='held',rate=rate,direction=direction,dt=dt,start=[300,260],end=end,
                                      scalar=struct.unpack('<f',e.read(actor+0x3B0,4))[0],
                                      position=struct.unpack('<ff',e.read(actor+0x338,8)))))


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
