"""Run 005F3D0A's full multi-NPC loop, shared history and batch construction.

Motion/collision, navigation feedback, endpoint forecast, effective-speed input
and external transport are controlled. 005F34EE/005F3695/list logic execute.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_ECX
from probe_npc_remote import create_remote_actor


def scenarios():
    def actor(index=0, **changes):
        a=dict(id=20001+index,position=[300+index*40,260],walking=True,direction=0,speed=2,
               corner=[0,0],end=[580-index*40,260],force=False)
        a.update(changes)
        if not a['walking']:
            a['corner']=[0,0]
            a['end']=[int(v) for v in a['position']]
        return a

    for direction in range(4):
        for speed in (1,2,3,10):
            yield f'unchanged_{direction}_{speed}',0,[
                dict(now=1000+i*40,dt=40,actors=[actor(direction=direction,speed=speed,position=[300+i*1.25,260])])
                for i in range(60)]
    yield 'stopped_subpixel',0,[dict(now=1000+i*20,dt=20,actors=[actor(walking=False,position=[300+i*.1,260.75],end=[300,260])]) for i in range(25)]
    yield 'end_changes',0,[dict(now=1000+i*20,dt=20,actors=[actor(position=[300+i,260],end=[580-i,260])]) for i in range(20)]
    corners=[[320,260],[325,260],[0,0],[0,260],[325,0],[330,260],[330,260],[0,0]]
    yield 'corner_rules',0,[dict(now=1000+i*20,dt=20,actors=[actor(position=[300+i,260],corner=c,end=[580 if i<6 else 540,260])]) for i,c in enumerate(corners)]
    yield 'state_changes',0,[dict(now=1000+i*20,dt=20,actors=[actor(position=[300+i,260],direction=i%4,speed=1+i%3,walking=i%3!=0)]) for i in range(24)]
    yield 'force',0,[dict(now=1000+i*20,dt=20,actors=[actor(position=[300+i,260],force=i in (3,8))]) for i in range(12)]
    yield 'sequence_boundary',65534,[dict(now=1000+i*20,dt=20,actors=[actor(direction=i%4)]) for i in range(6)]
    for count in (2,3):
        for mode in ('same','end','direction','speed','stop'):
            frames=[]
            for i in range(16):
                actors=[]
                for n in range(count):
                    changes=dict(position=[300+n*40+i,260])
                    if mode=='same':changes['end']=[580,260]
                    if mode=='direction':changes['direction']=n
                    if mode=='speed':changes['speed']=n+1
                    if mode=='stop':changes['walking']=False
                    actors.append(actor(n,**changes))
                frames.append(dict(now=1000+i*20,dt=20,actors=actors))
            yield f'multi_{count}_{mode}',0,frames


def run(root):
    e,world,actor,rule,controller=create_remote_actor(root)

    def patch(address,name,fn,pop):
        stub=e.add_stub(name)
        e.handlers[name]=fn,pop
        e.write(address,b'\xe9'+struct.pack('<i',stub-address-5))

    rule=e.make_object('publisher_rule',16,size=32)
    e.w32(world+0x4EA4,rule)
    e.handlers['publisher_rule+0x2c']=(lambda e:0),0
    actors={20001:actor,20002:e.malloc(0x1000),20003:e.malloc(0x1000)}
    states={}
    patch(0x5CF8D9,'controlled_motion',lambda e:1,7)
    patch(0x5CFB67,'controlled_directions',lambda e:0,7)
    patch(0x5B03F9,'navigation_feedback',lambda e:0,1)

    def forecast(e):
        s=states[e.arg(0)]
        for i,v in enumerate(s['corner']+s['end']):
            e.w32(e.arg(6+i),v)
        return 1

    patch(0x5CF9B8,'controlled_forecast',forecast,10)
    patch(0x87E6CA,'effective_speed_input',lambda e:states[e.uc.reg_read(UC_X86_REG_ECX)]['speed'],0)
    # These two cdecl calls reuse the same arguments at the end of the loop.
    patch(0x5A1DE1,'transport_encoding',lambda e:0,0)
    packets=[]

    def send(e):
        p=e.arg(1)
        count=e.read(p+2,1)[0]
        packets.append([dict(id=struct.unpack('<H',e.read(p+7+2*i,2))[0],
                             move=list(struct.unpack('<HI6HBB',e.read(p+0x27+20*i,20)))) for i in range(count)])
        return 1

    patch(0x5A2F4C,'capture_transport',send,0)
    vector=e.malloc(12)
    for name,initial_sequence,frames in scenarios():
        manager=e.malloc(0x100)
        e.call(0x5F3B59,ecx=manager)
        publisher=manager+0xC
        e.w32(manager+4,world)
        e.w32(publisher+4,initial_sequence)
        output=[]
        for frame in frames:
            for i,s in enumerate(frame['actors']):
                a=actors[s['id']]
                states[a]=s
                e.w32(vector+4*i,a)
                e.write(a+0x328,struct.pack('<H',s['id']))
                e.write(a+0x338,struct.pack('<ff',*s['position']))
                e.write(a+0x3B4,bytes([s['direction']]))
                e.write(a+0x3B8,bytes([2 if s['walking'] else 1]))
                e.w32(a+0x478,int(s['force']))
            e.w32(manager+0x90,vector)
            e.w32(manager+0x94,vector+4*len(frame['actors']))
            e.w32(world+0x3838,frame['now'])
            packets.clear()
            e.call(0x5F3D0A,(frame['dt'],),ecx=manager)
            assert len(packets)==1
            output.append(dict(**frame,sequence=e.r32(publisher+4),counter=e.r32(publisher+8),
                               history=e.r32(publisher+0x78),packet=packets[0]))
        print(json.dumps(dict(case=name,initial_sequence=initial_sequence,frames=output)))


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
