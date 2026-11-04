"""Default Boss owner motion and forecast using actual rule-1 tolerance.

Real 005F47A9 rule construction/getter, 005CF8D9 position update and 005CF9B8
forecast run. Grid predicates/terrain/passage and empty graphics are controlled.
AI decisions are inputs: this probes a held command between decisions.
"""

import argparse
import json
import pathlib
import struct

from probe_npc_motion import DELTAS, bits, motion_environment, signed


def run(root):
    e,world,actor,output,blocked,_=motion_environment(root)
    e.handlers['__dllonexit']=(lambda e:e.arg(0)),0
    e.w32(0x85DA74,e.r32(0x85DA74)|1)
    e.w32(0x806A78+0x7D4,world)
    rule=e.call(0x5F47A9,(world,1))
    getter=e.r32(e.r32(rule)+0x2C)
    tolerance=e.call(getter,ecx=rule)
    assert tolerance==6
    print(json.dumps(dict(kind='rule',rule=1,vtable=hex(e.r32(rule)),getter=hex(getter),tolerance=tolerance)))
    e.w32(actor,0x7A1D2C)
    e.write(actor+0x328,struct.pack('<H',30001))
    e.write(actor+0x394,bytes([32,7]))
    stub=e.add_stub('registered_boss')
    e.handlers['registered_boss']=(lambda e:1),1
    e.write(0x534AD0,b'\xe9'+struct.pack('<i',stub-0x534AD0-5))
    attrs=e.make_object('boss_motion_attributes',48,size=0x400)
    e.w32(actor+0x400,attrs)
    # Base speed is a controlled attribute input. The real VM getter invoked
    # by correction flags remains executable; no lazy memory mapping.
    scalar_output=e.malloc(4)

    def scalar(rate):
        wrapper=e.malloc(32)
        code=b'\x68'+struct.pack('<I',rate)+b'\xe8'+struct.pack('<i',0x5C452B-(wrapper+10))
        code+=b'\x83\xc4\x04\xd9\x1d'+struct.pack('<I',scalar_output)+b'\xc3'
        e.write(wrapper,code)
        e.call(wrapper)
        return struct.unpack('<f',e.read(scalar_output,4))[0]

    for direction,(dx,dy) in enumerate(DELTAS):
        front=(6+dy,5+dx)
        side=(front[0]-1 if dx else front[0],front[1]-1 if dy else front[1])
        cases=[]
        for offset in (-7,-6,-5,0,5,6,7,19,20):
            position=[220+(offset if dy else 0),260+(offset if dx else 0)]
            for walls in ([],[side],[front]):
                cases.append((position,walls,True))
        # The previous host stops at the neighbouring centre on this input.
        cases.append(([220+(39.75 if dx else 0),260+(39.75 if dy else 0)],[],True))
        cases.append(([220,260],[],False))
        for rate in (1,3,8):
            speed=scalar(rate)
            e.write(attrs+0x2B0,bytes([rate]))
            for start,walls,walking in cases:
                blocked.clear()
                blocked.update(walls)
                e.write(actor+0x338,struct.pack('<ff',*start))
                samples=[]
                for frame in range(16):
                    moved=e.call(0x5CF8D9,(actor,world,int(walking),direction,bits(speed),tolerance,20))
                    position=struct.unpack('<ff',e.read(actor+0x338,8))
                    e.call(0x5CF9B8,(actor,world,int(walking),direction,bits(speed),tolerance,
                                    output,output+4,output+8,output+12))
                    projection=[signed(e.r32(output+i)) for i in range(0,16,4)]
                    samples.append(dict(position=position,moved=moved,corner=projection[:2],end=projection[2:]))
                print(json.dumps(dict(kind='held',role=32,rate=rate,direction=direction,walking=walking,
                                      start=start,blocked=walls,dt=20,tolerance=tolerance,samples=samples)))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
