"""Execute raw PLAYER_MOVESEQ receive, native queue selection and correction.

005AF44D -> 005D0945 receives 20-byte little-endian decoded wire records;
005D09D6 advances the actual queue. No queue/state-selection hooks.
"""

import argparse
import json
import pathlib
import struct

from unicorn import UC_HOOK_CODE
from probe_npc_remote import create_remote_actor


def scenarios():
    def recv(seq, time, x=300, y=260, flags=0x10, speed=2, corner=(0,0), end=(580,260)):
        return dict(receive=dict(sequence=seq, time_stamp=time, current=[x,y], corner=list(corner),
                                 end=list(end), flags=flags, speed=speed))

    def frames(count=1, dt=20):
        return [dict(dt=dt) for _ in range(count)]

    yield 'same_sequence', [recv(1,1000)]+frames(3)+[recv(1,1020,320,speed=10,end=(500,260))]+frames(3)
    yield 'timestamp_order', [recv(1,0),recv(1,1000)]+frames()+[recv(2,1000,400),recv(2,999,500)]+frames(2)
    yield 'timestamp_wrap', [recv(1,0xFFFFFFF0)]+frames()+[recv(2,10,400)]+frames(2)
    yield 'future_timestamp', [recv(1,1000000)]+frames(4)
    yield 'buffered', [recv(1,1000),recv(1,1001,301),recv(2,1002,310),recv(3,1003,320)]+frames(8)
    yield 'gap', [recv(1,1000)]+frames(2)+[recv(6,1020,450)]+frames(3)
    yield 'force', [recv(1,1000)]+frames(3)+[recv(1,1020,400,flags=0x30)]+frames(3)
    yield 'far_same', [recv(1,1000)]+frames(2)+[recv(1,1020,500)]+frames(3)
    yield 'sequence_wrap', [recv(32767,1000),recv(32768,1020)]+frames(2)+[recv(65535,1040)]+frames()+[recv(0,1060)]+frames(3)
    yield 'corner_update', [recv(1,1000,corner=(320,260),end=(380,280))]+frames(10)+[recv(1,1020,320,corner=(330,260),end=(400,240))]+frames(25)
    for direction in range(4):
        vertical = direction & 1
        sign = -1 if direction in (1,2) else 1

        def xy(primary, cross):
            return (cross,primary) if vertical else (primary,cross)

        base = [recv(1,1000,*xy(300,260),flags=0x10|direction,end=xy(300+sign*200,260))]+frames()
        for speed in (1,2,3,10):
            for delta in (-41,-40,-10,-2,-1,0,1,2,8,9,40,41,81):
                for walking in (False,True):
                    name=f'dir{direction}_rate{speed}_delta{delta}_walk{int(walking)}'
                    target=xy(300+sign*delta,260)
                    yield name, base+[recv(2,1020,*target,flags=direction|(0x10 if walking else 0),speed=speed,
                                           end=xy(300+sign*200,260))]+frames(32)
        # Enter stop backtracking, then issue a new perpendicular walk while
        # correction is active. 005D0C7E has a vertical-direction quirk.
        for cross_delta in (-20,20):
            for next_dir in range(4):
                target=xy(300-sign*20,260)
                later=xy(300-sign*20,260+cross_delta)
                yield f'restart_{direction}_{cross_delta}_{next_dir}', base+[
                    recv(2,1020,*target,flags=direction),dict(dt=20),
                    recv(3,1040,*later,flags=0x10|next_dir,end=later)]+frames(24)


def run(root):
    e, scene, actor, rule, controller = create_remote_actor(root)
    e.handlers['abs'] = (lambda e: abs(struct.unpack('<i',struct.pack('<I',e.arg(0)))[0])), 0
    e.write(actor+0x328,struct.pack('<H',20001))
    e.write(actor+0x394,bytes([27,7]))
    e.w32(rule+8,8)
    remote, packet = e.malloc(0x80), e.malloc(20)
    consumed=[]
    e.uc.hook_add(UC_HOOK_CODE,lambda *args: consumed.append(True),begin=0x5D1348,end=0x5D1348)

    def snapshot():
        return dict(position=struct.unpack('<ff',e.read(actor+0x338,8)),count=e.r32(remote+0x14),
                    last_receive=e.r32(remote+0x18),walking=bool(e.r32(remote+0x1C)),direction=e.read(remote+0x20,1)[0],
                    rate=e.read(remote+0x21,1)[0],scalar=struct.unpack('<f',e.read(actor+0x3B0,4))[0],
                    phase=struct.unpack('<i',e.read(remote+0x34,4))[0],time=e.r32(remote+0x38),
                    sequence=struct.unpack('<i',e.read(remote+0x3C,4))[0],correction=e.r32(remote+0x44))

    for name, operations in scenarios():
        e.write(actor+0x338,struct.pack('<ff',300,260))
        e.w32(actor+0x3B0,0)
        e.write(actor+0x3B7,b'\1\1')
        e.call(0x5C06C9,(actor,),ecx=controller)
        e.call(0x5D086B,(actor,scene),ecx=remote)
        e.w32(actor+0x47C,remote)
        events=[]
        for op in operations:
            consumed.clear()
            if 'receive' in op:
                m=op['receive']
                e.write(packet,struct.pack('<HI6HBB',m['sequence'],m['time_stamp'],*m['current'],*m['corner'],*m['end'],m['flags'],m['speed']))
                e.call(0x5AF44D,(packet,),ecx=actor)
            else:
                e.call(0x5D09D6,(op['dt'],),ecx=remote)
            events.append(dict(**op,consumed=bool(consumed),state=snapshot()))
        print(json.dumps(dict(case=name,start=[300,260],events=events)))


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
