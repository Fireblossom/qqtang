"""Run gameUI base construction, alpha setters/getters, update and image draw.

Python script dispatch and image metadata/GPU draw are controlled. Attribute
loader writes were checked statically; this does not emulate Python2 loading.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_FPCW
from win32emu import Win32Emu


def run(root):
    e=Win32Emu(root/'gameUI.dll',root)
    e.uc.reg_write(UC_X86_REG_FPCW,0x007F)
    e.handlers['__dllonexit']=(lambda e:e.arg(0)),0

    def patch(address,name,function,pop):
        stub=e.add_stub(name)
        e.handlers[name]=function,pop
        e.write(address,b'\xe9'+struct.pack('<i',stub-address-5))

    events=[]
    patch(0x1002E949,'base_frame_script_dispatch',lambda e:0,2)
    patch(0x1002EA07,'script_callback',lambda e:events.append(e.cstr(e.arg(0)).decode()) or 0,2)
    patch(0x10013138,'image_canvas_width',lambda e:55,0)
    patch(0x10013160,'image_canvas_height',lambda e:55,0)
    draws=[]
    patch(0x10013238,'gpu_draw',lambda e:draws.append([e.arg(0),e.arg(1),e.arg(4)]) or 0,5)
    bridge=e.malloc(64)
    e.write(bridge,bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    for module in e.pe.DIRECTORY_ENTRY_IMPORT:
        for entry in module.imports:
            if entry.name==b'_ftol':e.w32(entry.address,bridge)

    def widget():
        w=e.malloc(0x400)
        e.call(0x100267B8,ecx=w)
        e.call(0x100279F0,(2,80,50,50),ecx=w)
        e.w32(w+0x98,e.malloc(32))
        return w

    def set_value(w,channel,value):
        e.call(0x10027580,(*struct.unpack('<II',struct.pack('<d',value)),channel),ecx=w)

    output=e.malloc(8)

    def get_value(w,channel):
        wrapper=e.malloc(32)
        code=b'\x68'+struct.pack('<I',channel)+b'\xb9'+struct.pack('<I',w)
        code+=b'\xe8'+struct.pack('<i',0x10027892-(wrapper+15))
        code+=b'\xdd\x1d'+struct.pack('<I',output)+b'\xc3'
        e.write(wrapper,code)
        e.call(wrapper)
        return struct.unpack('<d',e.read(output,8))[0]

    def state(w):
        return dict(speed=struct.unpack('<d',e.read(w+0x150,8))[0],alpha=struct.unpack('<d',e.read(w+0x158,8))[0],
                    minimum=struct.unpack('<d',e.read(w+0x160,8))[0],effective=struct.unpack('<d',e.read(w+0x168,8))[0],
                    direction=e.r32(w+0x170),get_speed=get_value(w,41),get_alpha=get_value(w,42))

    w=widget()
    print(json.dumps(dict(kind='defaults',state=state(w))))
    for channel,value in ((41,.1),(42,.8),(904,.6),(901,2),(901,0),(901,3),(901,1.5),(901,1)):
        set_value(w,channel,value)
        print(json.dumps(dict(kind='setter',channel=channel,value=value,state=state(w))))
    print(json.dumps(dict(kind='getters',values={str(c):get_value(w,c) for c in (41,42,901,904)})))
    for alpha in (0,.6,.8):
        for speed in (0,.01,.1,1):
            for minimum in (0,.6):
                for direction in (1,2):
                    for parent_alpha in (None,.5,.8):
                        w=widget()
                        for channel,value in ((42,alpha),(41,speed),(904,minimum),(901,direction)):
                            set_value(w,channel,value)
                        if parent_alpha is not None:
                            parent=widget()
                            set_value(parent,42,parent_alpha)
                            e.call(0x1002A017,ecx=parent)
                            e.w32(w+0x18,parent)
                        events.clear()
                        samples=[]
                        for frame in range(8):
                            e.call(0x1002A017,ecx=w)
                            draws.clear()
                            e.call(0x100297F3,ecx=w)
                            samples.append(dict(**state(w),disappear=events.count('OnDisappear'),draws=list(draws)))
                        print(json.dumps(dict(kind='fade',alpha=alpha,speed=speed,minimum=minimum,direction=direction,
                                              parent_alpha=parent_alpha,samples=samples)))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
