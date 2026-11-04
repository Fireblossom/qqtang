"""Execute DYSTRIP construction and shared 1001192F fill geometry.

Frame metadata and GPU draw are controlled; native win/texture rectangle
setters execute. Records describe draw geometry, not D3D raster pixels.
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
    e.call(0x10026279)  # original default draw color, white

    def patch(address,name,function,pop):
        stub=e.add_stub(name)
        e.handlers[name]=function,pop
        e.write(address,b'\xe9'+struct.pack('<i',stub-address-5))

    bridge=e.malloc(64)
    e.write(bridge,bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    for module in e.pe.DIRECTORY_ENTRY_IMPORT:
        for entry in module.imports:
            if entry.name==b'_ftol':e.w32(entry.address,bridge)
    patch(0x1002E949,'frame_script',lambda e:0,2)
    rect=e.malloc(16)
    patch(0x100131A7,'frame_metadata',lambda e:rect,1)
    draws=[]
    image=e.malloc(64)

    def draw(e):
        draws.append(dict(point=[e.arg(0),e.arg(1)],argb=e.arg(4),flags=e.r32(image+0x1C),
                          window=struct.unpack('<ffff',e.read(image+0x20,16)),
                          texture=struct.unpack('<ffff',e.read(image+0x30,16))))
        return 0

    patch(0x10013238,'gpu_draw',draw,5)
    for constructor,kind in ((0x10011788,'DYSTRIP'),(0x10011BF3,'DYLIFESTRIP')):
        w=e.malloc(0x400)
        e.call(constructor,ecx=w)
        print(json.dumps(dict(kind='constructor',type=kind,vtable=hex(e.r32(w)),
                              total=struct.unpack('<f',e.read(w+0x26C,4))[0],current=struct.unpack('<f',e.read(w+0x270,4))[0])))
    for source_w,source_h,width in ((15,15,110),(3,5,90)):
        e.write(rect,struct.pack('<iiii',0,0,source_w,source_h))
        for current in (-1,0,.01,.05,.1,1,2,3,5,8,10,11):
            for alpha in (.5,1):
                w=e.malloc(0x400)
                e.call(0x10011788,ecx=w)
                e.call(0x100279F0,(34,14,width,source_h),ecx=w)
                e.write(w+0x26C,struct.pack('<ff',10,current))
                e.write(w+0x158,struct.pack('<d',alpha))
                e.call(0x1002A017,ecx=w)
                e.write(image,b'\0'*64)
                e.w32(w+0x98,image)
                draws.clear()
                e.call(0x1001192F,ecx=w)
                print(json.dumps(dict(kind='draw',current=current,total=10,alpha=alpha,width=width,
                                      source=[source_w,source_h],point=[34,14],draws=draws)))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root',type=pathlib.Path)
    run(parser.parse_args().client_root)
