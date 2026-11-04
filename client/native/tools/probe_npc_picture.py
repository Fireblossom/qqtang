"""Execute the default model constructor, actor picture commands and draw chain.

00424460/00425730/00425D60, actor post-init, ice/walk/stop and
005B528F -> 00425220 -> 004264E0 execute. INI input, texture upload,
direction layout, viewport and the shadow device are controlled. No motion
7/14 data is installed unless an explicitly labelled getter requests it.
"""

import argparse
import configparser
import json
import pathlib
import struct
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from qqtclient.dimg import parse
from qqtclient.resources import Resources
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_ECX
from win32emu import Win32Emu


class PictureOracle:
    def __init__(self, root):
        self.e = e = Win32Emu(root / 'Client.exe', root)
        self.resources = Resources(root)
        e.handlers['__dllonexit'] = (lambda e: e.arg(0)), 0
        self.names = [e.cstr(e.call(0x448930, (slot,))).decode() for slot in range(19)]
        self.actions = [e.r32(0x7DB13C+i*4) for i in range(7)]
        self.sections = [e.cstr(e.call(0x4476D0, (a,))).decode() for a in self.actions]
        self.cloth_slot = self.names.index('cloth')
        self.assets, self.textures, self.clocks = {}, {}, {}
        self.draws = []
        clip = e.malloc(16)
        e.write(clip, struct.pack('<iiii', -1000, -1000, 3000, 3000))
        self.patch(0x453690, 'style_input', self.style_input, 1)
        self.patch(0x4254A0, 'direction_layout', lambda e: 0, 3)
        self.patch(0x487AE0, 'texture_upload', self.texture_upload, 2)
        self.patch(0x427250, 'frame_count', self.frame_count, 0)
        self.patch(0x427170, 'render_setup', lambda e: 0, 0)
        self.patch(0x427600, 'viewport', lambda e: clip, 0)
        self.patch(0x4878A0, 'bounds', self.bounds, 4)
        self.patch(0x4875C0, 'draw_texture', self.draw_texture, 6)
        self.patch(0x42A340, 'shadow_device', self.shadow_device, 3)
        self.patch(0x534AD0, 'is_npc', lambda e: 1, 1)
        self.patch(0x534990, 'is_local', lambda e: 1, 1)
        # Empty auxiliary parts have the same sort key. Texture order and
        # rasterization are outside this probe; body availability is observed.
        e.handlers['qsort'] = (lambda e: 0), 0
        bridge = e.malloc(64)
        e.write(bridge, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
        e.write(0x6200CE, b'\xe9'+struct.pack('<i', bridge-0x6200CE-5))
        e.uc.hook_add(UC_HOOK_CODE, self.observe_reset, begin=0x426240, end=0x426240)
        e.uc.hook_add(UC_HOOK_CODE, self.observe_update, begin=0x4262F0, end=0x4262F0)
        buf = e.malloc(512)
        identifier = e.call(0x4253D0, (7, 131))
        e.call(0x61FD25, (identifier, buf))
        self.shadow_path = e.cstr(buf).decode().replace('\\', '/')
        self.shadow_id = identifier

    def patch(self, address, name, function, pop):
        e = self.e
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9'+struct.pack('<i', stub-address-5))

    def style_input(self, e):
        style = e.uc.reg_read(UC_X86_REG_ECX)
        e.w32(style, 0x7995B4)
        e.write(style+4, bytes([e.arg(0) & 255]))
        for slot, name in enumerate(self.names):
            for index, section in enumerate(self.sections):
                e.w32(style+8+slot*28+index*4, self.ini.getint(section, name, fallback=0))
        return style

    def texture_upload(self, e):
        data = e.uc.reg_read(UC_X86_REG_ECX)
        self.textures[data] = self.assets.get(e.arg(0))
        return 1

    def frame_count(self, e):
        asset = self.textures.get(e.uc.reg_read(UC_X86_REG_ECX))
        return max(1, asset['frames']) if asset else 1

    def bounds(self, e):
        e.write(e.arg(0), struct.pack('<iiii', 0, 0, 100, 100))
        return 1

    def draw_texture(self, e):
        asset = self.textures.get(e.uc.reg_read(UC_X86_REG_ECX))
        if asset and asset['exists']:
            self.draws.append(asset['path'])
        return 1

    def shadow_device(self, e):
        ptr = e.uc.reg_read(UC_X86_REG_ECX)
        assert e.arg(1) == self.shadow_id
        name = f'shadow{ptr}'
        obj = e.make_object(name, 4, size=0x80)
        e.write(ptr, e.read(obj, 0x80))
        e.handlers[name+'+0x4'] = (lambda e: 0), 1
        e.handlers[name+'+0x8'] = (lambda e: self.draws.append(self.shadow_path) or 0), 0
        return ptr

    def observe_reset(self, uc, address, size, user):
        self.clocks[uc.reg_read(UC_X86_REG_ECX)] = 0

    def observe_update(self, uc, address, size, user):
        part = uc.reg_read(UC_X86_REG_ECX)
        if self.e.read(part+0x38, 1)[0] == 0:
            self.clocks[part] = self.clocks.get(part, 0)+self.e.arg(0)

    def make(self, role):
        e = self.e
        self.role = role
        self.ini = configparser.ConfigParser()
        self.ini.read_string(self.resources.read(f'object/player/player{role}.ini').decode('gbk'))
        pathbuf = e.malloc(512)
        for slot, name in enumerate(self.names):
            base = self.ini.getint('stand', name, fallback=0)
            if not base:
                continue
            for action in range(1, 16):
                identifier = e.call(0x4269F0, (slot, base, action))
                e.call(0x61FD25, (identifier, pathbuf))
                path = e.cstr(pathbuf).decode().replace('\\', '/')
                raw = self.resources.read(path)
                sprite = parse(raw) if raw else None
                self.assets[identifier] = dict(path=path, exists=bool(raw),
                                                frames=len(sprite.frames)//sprite.directions if sprite else 0)
        self.avatar = e.malloc(0x330)
        e.call(0x424460, (role, 6), ecx=self.avatar)
        self.part = e.r32(self.avatar+0x278+self.cloth_slot*4)
        self.actor, world, human, rule, self.controller = [e.malloc(n) for n in (0x1000, 0x6000, 32, 32, 32)]
        e.w32(0x85DA74, e.r32(0x85DA74) | 1)
        e.w32(0x806A78+0x7D4, world)
        e.w32(world+0x37F8, world)
        e.w32(world+0x4EA4, rule)
        e.w32(rule+8, 8)
        e.w32(world+0x3824, human)
        e.w32(world+0x3830, human)
        e.w32(self.actor, 0x7A1D2C)
        e.write(self.actor+0x328, struct.pack('<H', 20001 if role in (25, 26, 27) else 30001))
        e.write(self.actor+0x394, bytes([role, 7]))
        e.w32(self.actor+0x31C, self.avatar)
        e.w32(self.actor+0x3FC, world)
        e.w32(self.actor+0x36C, self.controller)
        e.w32(self.actor+0x3CC, -5001)
        e.write(self.actor+0x3B7, b'\x01\x01')
        e.write(self.actor+0x3B4, b'\x03')
        e.write(self.actor+0x338, struct.pack('<ff', 220, 260))
        attrs = e.make_object(f'attrs{role}', 48, size=0x400)
        e.w32(self.actor+0x400, attrs)
        e.call(0x5C06C9, (self.actor,), ecx=self.controller)
        e.call(0x5AF9F3, ecx=self.actor)
        self.clocks[self.part] = 0

    def snapshot(self, operation, dt=0, direction=3, updates=1, kind='event'):
        e = self.e
        self.draws.clear()
        e.call(0x5B528F, ecx=self.actor+0x318)
        shadow = e.r32(self.avatar+0x320)
        action = e.r32(self.part+0x14)
        data = e.r32(self.part+0x3C+action*4)
        asset = self.textures.get(data)
        row = dict(kind=kind, role=self.role, operation=operation, dt=dt, direction=direction, updates=updates,
                    action=action, facing=e.r32(self.avatar+0x26C)//90,
                    clock=self.clocks[self.part], frame=e.r32(self.part+0x28), remainder=e.r32(self.part+0x2C),
                    data_present=bool(data), image=asset, draws=list(self.draws),
                    shadow_position=struct.unpack('<ii', e.read(shadow+8, 8)),
                    cloth_position=struct.unpack('<ii', e.read(self.part+0x7C, 8)),
                    ice_active=bool(e.r32(self.actor+0x3E8)))
        row['loaded_actions'] = [a for a in range(16) if e.r32(self.part+0x3C+4*a)]
        return row

    def run(self):
        e = self.e
        for role in (25, 26, 27, 24, 28, 32, 34, 35, 36):
            self.make(role)
            print(json.dumps(self.snapshot('init')))
            for operation, dt, direction in (
                ('walk', 20, 0), ('hold', 80, 0), ('walk', 20, 0), ('walk', 20, 1),
                ('ice', 20, 1), ('hold', 200, 1), ('ice', 20, 1), ('walk', 20, 1),
                ('stop', 20, 1), ('ice', 20, 1), ('stop', 20, 1),
            ):
                if operation == 'walk':
                    e.call(0x5C07FE, (direction,), ecx=self.controller)
                elif operation == 'stop':
                    e.call(0x5C07D6, ecx=self.controller)
                elif operation == 'ice':
                    e.call(0x5ACBB7, (0,), ecx=self.actor)
                e.call(0x5ABE96, (dt,), ecx=self.actor)
                print(json.dumps(self.snapshot(operation, dt, direction)))
            e.call(0x5C07FE, (1,), ecx=self.controller)
            for frame in range(501):
                if frame % 12 == 0:
                    e.call(0x5C07FE, (1,), ecx=self.controller)
                e.call(0x5ABE96, (20,), ecx=self.actor)
            print(json.dumps(self.snapshot('walk_expiry', 20, 1, updates=501)))
            e.call(0x5ACBFC, ecx=self.actor)
            e.call(0x5B5244, (20,), ecx=self.actor+0x318)
            print(json.dumps(self.snapshot('birth', 20, 3)))
            # Explicit get-image-data is a different path from normal construction.
            e.call(0x4268F0, (14,), ecx=self.part)
            e.call(0x5B5244, (200,), ecx=self.actor+0x318)
            print(json.dumps(self.snapshot('load_birth', 200, 3, kind='explicit')))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    PictureOracle(parser.parse_args().client_root).run()
