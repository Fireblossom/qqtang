"""PVE initialized route consumption, rule registration and default owner motion.

Real PVE construction/VM initialization precedes 005BB9F1/005BB7F6/005BB1E1.
World queries and failed fallback search are controlled. Physical samples run
005CF8D9/005CF9B8 with controlled static geometry and real default attributes.
"""

import argparse
import json
import pathlib
import struct

from probe_pve_remote import create_pve_actor
from probe_npc_motion import DELTAS, bits, signed


def run(root):
    e,world,actor,remote,witness,patch=create_pve_actor(root,initial_cell=(7,6),region_bounds=(0,0,14,12))
    e.handlers['abs']=(lambda e:abs(signed(e.arg(0)))),0
    scene=world-0x1310
    patch(0x5C378F,'width',lambda e:15,0)
    patch(0x5C3795,'height',lambda e:13,0)
    commands=[]

    def register(e):
        command=e.arg(0)
        e.w32(command+4,scene)
        commands.append(command)
        return 1

    patch(0x5F4350,'register_pve_commands',register,1)
    rule=e.call(0x5F47A9,(scene,12))
    e.w32(scene+0x4EA4,rule)
    setup=e.call(e.r32(e.r32(rule)+8),ecx=rule)&255
    tolerance=e.call(e.r32(e.r32(rule)+0x2C),ecx=rule)
    rate=e.call(0x87E6CA,ecx=actor)
    records=[dict(vtable=hex(e.r32(c)),category=e.call(e.r32(e.r32(c)+4),ecx=c),
                  execute=hex(e.r32(e.r32(c)+8))) for c in commands]
    assert setup==1 and tolerance==6 and rate==5
    print(json.dumps(dict(kind='init',witness=witness,initial_cell=[7,6],region_bounds=[0,0,14,12],mode=e.read(actor+0x3B7,1)[0],rate=rate,
                          rule_vtable=hex(e.r32(rule)),tolerance=tolerance,commands=records)))
    empty,occupied,token=[e.malloc(n) for n in (16,16,4)]
    e.w32(occupied+4,token)
    e.w32(occupied+8,token+4)
    queries=[]
    state=dict(bombs=set(),elements=set(),blocked=set())

    def bombs(e):
        row,col=e.arg(0)&255,e.arg(1)&255
        queries.append([0,col,row,0])
        return occupied if (col,row) in state['bombs'] else empty

    element=e.make_object('pve_route_element',16,size=32)

    def element_at(e):
        row,col=e.arg(0)&255,e.arg(1)&255
        queries.append([1,col,row,0])
        return element if (col,row) in state['elements'] else 0

    def element_blocked(e):
        row,col,direction=e.arg(0)&255,e.arg(1)&255,e.arg(2)&255
        queries.append([2,col,row,direction])
        return int((col,row) in state['blocked'])

    e.handlers['pve_route_element+0x2c']=element_blocked,3
    patch(0x5D3BD3,'route_bombs',bombs,2)
    patch(0x5D865C,'route_elements',element_at,2)
    patch(0x4522F0,'route_rng',lambda e:0,0)
    finder=e.make_object('failed_route_finder',4,size=32)
    searches=[]

    def failed_search(e):
        searches.append(True)
        return 0

    e.handlers['failed_route_finder+0x4']=failed_search,3
    e.w32(actor+0x8F8,finder)
    target=e.malloc(0x1000)
    vector=e.malloc(256)

    def put_route(nodes):
        e.write(vector,b''.join(bytes(n) for n in nodes))
        e.w32(actor+0x900,vector)
        e.w32(actor+0x904,vector+3*len(nodes))
        e.w32(actor+0x908,vector+256)

    def read_route():
        return [list(e.read(p,3)) for p in range(e.r32(actor+0x900),e.r32(actor+0x904),3)]

    for direction,(dx,dy) in enumerate(DELTAS):
        nodes=[[7,6,direction],[7+dx,6+dy,direction],[7+2*dx,6+2*dy,9]]
        goal=nodes[-1][:2]
        for offset in (-20,-10,0,10,19.75,20,20.25,40,60,60.25):
            position=[300+dx*offset,260+dy*offset]
            for condition in ('open','bomb_head','bomb_next','element_head_block','element_next_open',
                              'battle_no_target','battle_moved','battle_same'):
                state.update(bombs=set(),elements=set(),blocked=set())
                if condition.startswith('bomb_'):
                    state['bombs'].add(tuple(nodes[0 if condition=='bomb_head' else 1][:2]))
                if condition.startswith('element_'):
                    cell=tuple(nodes[0 if condition=='element_head_block' else 1][:2])
                    state['elements'].add(cell)
                    if condition=='element_head_block':state['blocked'].add(cell)
                battle=condition.startswith('battle_')
                present=battle and condition!='battle_no_target'
                target_cell=[goal[0]+int(condition=='battle_moved'),goal[1]]
                for method,function in (('find',0x5BB9F1),('valid',0x5BB7F6),('strategy3',0x5BB1E1)):
                    put_route(nodes)
                    e.write(actor+0x338,struct.pack('<ff',*position))
                    e.w32(actor+0x8A8,int(battle))
                    e.w32(actor+0x8E4,target if present else 0)
                    e.write(actor+0x8EC,bytes([*goal,0]))
                    e.write(target+0x338,struct.pack('<ff',target_cell[0]*40+20,target_cell[1]*40+20))
                    center=e.call(0x5BB932,ecx=actor)&255
                    queries.clear()
                    searches.clear()
                    result=e.call(function,ecx=actor)&255
                    print(json.dumps(dict(kind='route',method=method,direction=direction,condition=condition,
                                          position=position,before=nodes,goal=goal,battle=battle,target_present=present,
                                          target_cell=target_cell,bombs=sorted(state['bombs']),elements=sorted(state['elements']),
                                          blocked=sorted(state['blocked']),center=bool(center),result=result,after=read_route(),
                                          queries=queries,fallback=bool(searches))))
    # Actual initialized PVE owner motion; the default capability getter and
    # movement/forecast wrappers run, while static leading-edge queries are controlled.
    geometry=set()

    def can_pass(e):
        x,y=[struct.unpack('<h',struct.pack('<H',e.arg(i)&65535))[0] for i in range(2)]
        direction=e.arg(2)&255
        dx,dy=DELTAS[direction]
        points=[(x+19*dx,y-19),(x+19*dx,y+19)] if dx else [(x-19,y+19*dy),(x+19,y+19*dy)]
        return int(all(0<=a<600 and 0<=b<520 and (b//40,a//40) not in geometry for a,b in points))

    patch(0x5B7999,'pve_geometry',can_pass,6)
    patch(0x5D8BC1,'pve_terrain_exit',lambda e:1,3)
    output=e.malloc(16)
    scalar_output=e.malloc(4)
    wrapper=e.malloc(32)
    code=b'\x68'+struct.pack('<I',rate)+b'\xe8'+struct.pack('<i',0x5C452B-(wrapper+10))
    code+=b'\x83\xc4\x04\xd9\x1d'+struct.pack('<I',scalar_output)+b'\xc3'
    e.write(wrapper,code)
    e.call(wrapper)
    speed=struct.unpack('<f',e.read(scalar_output,4))[0]
    for direction,(dx,dy) in enumerate(DELTAS):
        front=(6+dy,5+dx)
        side=(front[0]-1 if dx else front[0],front[1]-1 if dy else front[1])
        e.call(0x5C07FE,(direction,),ecx=e.r32(actor+0x36C))
        assert e.read(actor+0x3B8,1)[0]==2 and e.read(actor+0x3B4,1)[0]==direction
        for offset in (-7,-6,0,6,7,19):
            position=[220+(offset if dy else 0),260+(offset if dx else 0)]
            for walls in ([],[front],[side]):
                geometry.clear()
                geometry.update(walls)
                e.write(actor+0x338,struct.pack('<ff',*position))
                samples=[]
                for frame in range(16):
                    moved=e.call(0x5CF8D9,(actor,world,1,direction,bits(speed),tolerance,20))
                    actual=struct.unpack('<ff',e.read(actor+0x338,8))
                    e.call(0x5CF9B8,(actor,world,1,direction,bits(speed),tolerance,output,output+4,output+8,output+12))
                    projection=[signed(e.r32(output+i)) for i in range(0,16,4)]
                    samples.append(dict(position=actual,moved=moved,corner=projection[:2],end=projection[2:]))
                print(json.dumps(dict(kind='motion',rate=rate,speed=speed,direction=direction,start=position,
                                      blocked=walls,dt=20,tolerance=tolerance,samples=samples)))
    # The prepare receiver sets the incoming facing, snaps to the prop cell,
    # then calls real 005F8624. Remote held state is a distinct controller.
    e.w32(actor+0x8A8,0)
    e.w32(actor+0x8E4,0)
    e.w32(actor+0x904,e.r32(actor+0x900))
    packet=e.malloc(0x40)
    e.w32(packet+8,1)
    e.write(packet+0x10,bytes([0,2]))
    e.w32(packet+0x2C,580)
    e.w32(packet+0x30,260)
    e.write(actor+0x338,struct.pack('<ff',300,260))
    e.call(0x5D1348,(packet,),ecx=remote)

    def pose():
        return dict(position=struct.unpack('<ff',e.read(actor+0x338,8)),facing=e.read(actor+0x3B4,1)[0],
                    walk_flag=e.read(actor+0x3B8,1)[0],movement_mode=e.call(0x5C0BDA,ecx=e.r32(actor+0x36C)),
                    held_walk=e.r32(remote+0x1C),attribute_rate=e.call(0x87E6CA,ecx=actor))

    e.call(0x5BACC0,(20,),ecx=actor)
    before=pose()
    e.write(actor+0x3B4,b'\3')
    e.call(0x5AD322,(bits(300),bits(260)),ecx=actor)
    e.call(0x5F8624,(actor,))
    stopped=pose()
    e.call(0x5BACC0,(20,),ecx=actor)
    print(json.dumps(dict(kind='prepare_stop',before=before,stopped=stopped,after=pose(),dt=20)))
    # A second actually initialized NPC shares the same registered scene
    # movement command. Selector outputs are controlled; state setters run.
    for offset in (0x8C,0x94):
        e.call(e.r32(e.r32(actor)+offset),ecx=actor)
    second=e.malloc(0x1000)
    e.call(0x5BA4F2,(world,30301,26,9),ecx=second)
    entry=e.malloc(0x48)
    for offset,value in {0:30301,4:30047,8:1,0x20:26,0x24:3,0x28:500,0x2C:10001,
                         0x30:5,0x34:401,0x38:2,0x3C:7,0x40:6,0x44:6}.items():
        e.w32(entry+offset,value)
    e.write(entry+0xC,b'NAME\0')
    initialized=e.call(0x5BC907,(entry,),ecx=second)&255
    assert initialized==1 and e.r32(e.r32(second+0x47C)+4)==second
    for offset in (0x31C,0x40C,0x48C,0x418):e.w32(second+offset,0)
    ui=e.make_object('pve_command_ui',80,size=32)
    e.w32(0x7FA410,ui)
    e.handlers['pve_command_ui+0x44']=(lambda e:0),1
    gate_events=[]

    def gate_battle(e):
        gate_events.append('battle')
        return 0

    def gate_skill(e):
        gate_events.append('skill')
        return 0

    patch(0x5BADE5,'controlled_battle_update',gate_battle,0)
    patch(e.r32(e.r32(actor)+0xE0),'controlled_skill_check',gate_skill,0)
    e.w32(actor+0x93C,0)
    e.w32(actor+0x940,1)
    result=e.call(0x5BAE70,ecx=actor)&255
    assert result==9 and gate_events==['battle','skill']
    print(json.dumps(dict(kind='move_gate',can_move=0,can_skill=1,result=result,events=gate_events)))
    e.w32(actor+0x93C,1)
    selected=[]
    answer=[9]

    def decide(e):
        selected.append(True)
        return answer[0]

    patch(0x5BAE70,'controlled_pve_selector',decide,0)
    # The real selector was executed by the gate sample above. Explicitly
    # invalidate its translated blocks before replacing it with a callback.
    e.uc.ctl_remove_cache(0x5BAE70,0x5BAEDF)
    command=next(c for c in commands if e.r32(e.r32(c)+8)==0x6112AF)
    e.write(command+8,b'\5')
    e.w32(scene+0x4B48,61000)
    for a in (actor,second):e.call(0x5C07FE,(0,),ecx=e.r32(a+0x36C))
    steps=[]
    for a,direction in ((actor,9),(second,9),(actor,9),(second,0)):
        answer[0]=direction
        selected.clear()
        before=e.read(command+8,1)[0]
        e.call(0x6112AF,(a,),ecx=command)
        assert len(selected)==int(before>5)
        steps.append(dict(id=struct.unpack('<H',e.read(a+0x328,2))[0],direction=direction,before=before,
                          after=e.read(command+8,1)[0],decisions=len(selected),walk_flag=e.read(a+0x3B8,1)[0],
                          facing=e.read(a+0x3B4,1)[0]))
    print(json.dumps(dict(kind='shared_command',second_initialized=initialized,steps=steps)))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
