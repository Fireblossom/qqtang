"""Observe actual gameUI constructor colors and base-caption ARGB.

The font layout outlet is controlled; global color initialization, complete
widget constructors and 10026E0E caption alpha modulation execute unchanged.
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
    e.call(0x10026279)
    bridge = e.malloc(64)
    e.write(bridge, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    for module in e.pe.DIRECTORY_ENTRY_IMPORT:
        for entry in module.imports:
            if entry.name == b'_ftol':
                e.w32(entry.address, bridge)
    draws = []
    stub = e.add_stub('font_layout')
    e.handlers['font_layout'] = (lambda e: draws.append(e.arg(7)) or 0), 0
    e.write(0x10021E86, b'\xe9' + struct.pack('<i', stub - 0x10021E86 - 5))
    for kind, constructor in (('WIDGET', 0x100267B8), ('BUTTON', 0x10003F1D),
                              ('CHECK', 0x100048AC), ('LABEL', 0x100050B8)):
        w = e.malloc(0x400)
        e.call(constructor, ecx=w)
        print(json.dumps(dict(kind='default', type=kind, draw_color=e.r32(w + 0xCC))))
    text = e.malloc(32)
    e.write(text, b'Native text\0')
    for color in (None, (0, 0, 0, 255), (32, 64, 128, 153)):
        for alpha in (0, .5, .8, 1):
            w = e.malloc(0x400)
            e.call(0x100267B8, ecx=w)
            if color is not None:
                r, g, b, a = color
                e.w32(w + 0xCC, a << 24 | r << 16 | g << 8 | b)
            e.write(w + 0x168, struct.pack('<d', alpha))
            draws.clear()
            e.call(0x10026E0E, (text,), ecx=w)
            print(json.dumps(dict(kind='caption', color=color, alpha=alpha, draws=list(draws))))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
