"""Execute gameUI widget animation, default schemes and image overflow draw.

Script property loading is controlled (tuple pairs are deinterleaved as in
1002B42F). Image readiness/count and the GPU outlet are controlled; the actual
10013238 wrapper clamps frames and returns its overflow flag to 100297F3.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_FPCW
from win32emu import Win32Emu


def run(root):
    e = Win32Emu(root / 'gameUI.dll', root)
    e.uc.reg_write(UC_X86_REG_FPCW, 0x007F)
    e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9' + struct.pack('<i', stub - address - 5))

    count = 7
    patch(0x10012BF2, 'image_ready', lambda e: 0, 0)
    patch(0x100139E9, 'image_count', lambda e: count, 0)
    patch(0x10013138, 'canvas_width', lambda e: 50, 0)
    patch(0x10013160, 'canvas_height', lambda e: 50, 0)
    bridge = e.malloc(64)
    e.write(bridge, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    for module in e.pe.DIRECTORY_ENTRY_IMPORT:
        for entry in module.imports:
            if entry.name == b'_ftol':
                e.w32(entry.address, bridge)
    gfx = e.make_object('gfx', 8, size=32)
    e.w32(0x1004856C, gfx)
    draws = []
    e.handlers['gfx+0x10'] = (lambda e: draws.append(e.arg(2)) or 0), 7
    host, frames = e.malloc(0x100), e.malloc(24 * 7)
    e.w32(host + 0x48, frames)
    e.w32(host + 0x84, 1)
    for i in range(7):
        e.write(frames + 24 * i, struct.pack('<6i', 0, 0, 50, 50, 0, 0))
    image = e.malloc(64)
    e.w32(image + 8, host)

    def double(w, offset):
        return struct.unpack('<d', e.read(w + offset, 8))[0]

    def signed(w, offset):
        return struct.unpack('<i', e.read(w + offset, 4))[0]

    def state(w):
        return dict(frame=signed(w, 0xE8), state=e.r32(w + 0x1F4), row=e.r32(w + 0x1F0),
                    frame_age=double(w, 0x138), loop_age=double(w, 0x140), hidden=bool(e.read(w + 0x94, 1)[0]))

    constructors = {'WIDGET': 0x100267B8, 'BUTTON': 0x10003F1D, 'CHECK': 0x100048AC}

    def create(kind, scheme=None):
        w = e.malloc(0x400)
        e.call(constructors[kind], ecx=w)
        e.w32(w + 0x98, image)
        if scheme is None:
            e.call(e.r32(e.r32(w) + 0xB0), ecx=w)
        else:
            for row in scheme:
                data = e.malloc(32)
                e.write(data, struct.pack('<8i', *(row[::2] + row[1::2])))
                e.call(0x1000FA3A, (data,), ecx=w + 0x1E0)
        return w

    for kind in constructors:
        w = create(kind)
        start, end = e.r32(w + 0x1E4), e.r32(w + 0x1E8)
        rows = []
        for address in range(start, end, 32):
            native = struct.unpack('<8i', e.read(address, 32))
            rows.append([value for pair in zip(native[:4], native[4:]) for value in pair])
        print(json.dumps(dict(kind='defaults', type=kind, rows=rows, speed=double(w, 0x1F8),
                              loop_speed=double(w, 0x148), initial=state(w))))

    schemes = [None, [[0, 99] * 4], [[0, 0, 1, 1, 2, 5, 6, 6]],
               [[0, 0, 0, 0, -1, -1, -1, -1], [2, 4] * 4]]
    for kind in constructors:
        for scheme in schemes:
            for speed in (.15, .08, .25, 0):
                for loop_speed in (0, .4, 4):
                    for image_flag in (0, 8):
                        count = 3 if speed == .25 else 7
                        w = create(kind, scheme)
                        e.write(w + 0x1F8, struct.pack('<d', speed))
                        e.write(w + 0x148, struct.pack('<d', loop_speed))
                        e.w32(w + 0xC4, image_flag)
                        samples = []
                        for i in range(48):
                            # Includes pressed without hover, state changes,
                            # same-state CHECK row changes and disabled flags.
                            flags = (0x80, 0x81, 0x82, 0x88, 0x90, 0x91, 0x80, 0x80)[i // 6]
                            dt = (0, .016, .08, .15, .25, .7)[i % 6]
                            e.w32(w + 0xE4, flags)
                            dt_bits = struct.unpack('<I', struct.pack('<f', dt))[0]
                            e.call(e.r32(e.r32(w) + 0x54), (dt_bits,), ecx=w)
                            before = state(w)
                            draws.clear()
                            e.call(0x100297F3, ecx=w)
                            samples.append(dict(flags=flags, dt=dt, before=before, after=state(w), draws=list(draws)))
                        print(json.dumps(dict(kind='sequence', type=kind, scheme=scheme, speed=speed,
                                              loop_speed=loop_speed, count=count, image_flag=image_flag, samples=samples)))

    # Actual Win_SetImg target. Image identity lookup/release are controlled;
    # An empty replacement needs no resource loader or GPU constructor.
    name = e.malloc(32)
    e.write(name, b'frames.img\0')
    patch(0x1001312B, 'image_name', lambda e: name, 0)
    patch(0x1003570E, 'release_image', lambda e: 0, 0)
    for new_name in ('frames.img', ''):
        w = create('WIDGET')
        e.w32(w + 0xE8, 4)
        e.write(w + 0x94, b'\x01')
        e.write(w + 0x138, struct.pack('<dd', .125, 1.25))
        before = state(w)
        data = e.malloc(32)
        e.write(data, new_name.encode() + b'\0')
        e.call(0x10028871, (data,), ecx=w)
        print(json.dumps(dict(kind='image_change', image=new_name, before=before, after=state(w))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
