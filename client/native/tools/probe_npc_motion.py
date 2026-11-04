"""Execute original float movement and forecast with a controlled static grid.

005CFC10 and 005CF9B8 execute unchanged. Collision predicate, terrain exits,
map dimensions and passage capability are controlled dependencies.
"""

import argparse
import json
import pathlib
import struct

from unicorn.x86_const import UC_X86_REG_FPCW

from win32emu import Win32Emu


DELTAS = [(1, 0), (0, -1), (-1, 0), (0, 1)]


def bits(value):
    return struct.unpack('<I', struct.pack('<f', value))[0]


def signed(value):
    return value if value < 2**31 else value - 2**32


def motion_environment(root):
    e = Win32Emu(root / 'Client.exe', root)
    # GFX CreateDevice omits FPU_PRESERVE: single precision, round-nearest.
    e.uc.reg_write(UC_X86_REG_FPCW, 0x007F)
    e.handlers['abs'] = (lambda e: abs(signed(e.arg(0)))), 0
    blocked = set()

    def patch(address, name, function, pop):
        stub = e.add_stub(name)
        e.handlers[name] = function, pop
        e.write(address, b'\xe9' + struct.pack('<i', stub - address - 5))

    def can_pass(e):
        # 005B7999 -> 005B7D1B reads signed SHORT coordinates; upper halves
        # can contain a stale pointer, notably in the Up branch of 005CFC10.
        x, y = [struct.unpack('<h', struct.pack('<H', e.arg(i) & 65535))[0] for i in range(2)]
        direction = e.arg(2) & 255
        dx, dy = DELTAS[direction]
        if dx:
            points = [(x + 19*dx, y - 19), (x + 19*dx, y + 19)]
        else:
            points = [(x - 19, y + 19*dy), (x + 19, y + 19*dy)]
        return int(all(0 <= a < 600 and 0 <= b < 520 and (b//40, a//40) not in blocked
                       for a, b in points))

    patch(0x5B7999, 'can_pass', can_pass, 6)
    patch(0x5D8BC1, 'terrain', lambda e: 1, 3)
    patch(0x5C378F, 'width', lambda e: 15, 0)
    patch(0x5C3795, 'height', lambda e: 13, 0)
    patch(0x5ACF9E, 'passage', lambda e: 0, 0)
    code = e.malloc(64)
    e.write(code, bytes.fromhex('83 ec 10 d9 3c 24 66 8b 04 24 80 cc 0c 66 89 44 24 02 d9 6c 24 02 df 7c 24 08 d9 2c 24 8b 44 24 08 8b 54 24 0c 83 c4 10 c3'))
    e.write(0x6200CE, b'\xe9' + struct.pack('<i', code - 0x6200CE - 5))
    scene, actor, output = [e.malloc(n) for n in (0x6000, 0x1000, 32)]

    def move(position, direction, dt, speed, tolerance=10):
        dx, dy = DELTAS[direction]
        moved = e.call(0x5CFC10, (scene, bits(position[0]), bits(position[1]), 0, direction,
                                 bits(speed*dx), bits(speed*dy), dt, tolerance,
                                 output, output+4, output+8, output+12))
        result = struct.unpack('<ff', e.read(output, 8))
        return moved, result

    return e, scene, actor, output, blocked, move


def run(root):
    e, scene, actor, output, blocked, move = motion_environment(root)

    def record(position, direction, walls, forecast=True):
        blocked.clear()
        blocked.update(walls)
        parameters = [(1, 1), (20, 3.077), (25, 13.333), (1000, 3.077)] if forecast else [(20, 3.077), (1000, 3.077)]
        for dt, speed in parameters:
            moved, result = move(position, direction, dt, speed)
            print(json.dumps(dict(kind='move', position=position, direction=direction, dt=dt,
                                  speed=speed, blocked=walls, result=result, moved=moved)))
        if forecast:
            e.write(actor+0x338, struct.pack('<ff', *position))
            e.call(0x5CF9B8, (actor, scene, 1, direction, bits(3.077), 10,
                             output, output+4, output+8, output+12), limit=5_000_000)
            result = [signed(e.r32(output+i)) for i in range(0, 16, 4)]
            print(json.dumps(dict(kind='forecast', position=position, direction=direction,
                                  speed=3.077, blocked=walls, result=result)))

    for direction, (dx, dy) in enumerate(DELTAS):
        front = (6+dy, 5+dx)
        negative = (front[0]-1 if dx else front[0], front[1]-1 if dy else front[1])
        positive = (front[0]+1 if dx else front[0], front[1]+1 if dy else front[1])
        layouts = [[], [negative], [positive], [front], [front, negative],
                   [front, positive], [front, negative, positive]]
        for offset in [-20, -19, -11, -10, -9, -.25, 0, .5, 9, 10, 11, 19, 20]:
            position = (220+offset if dy else 220, 260+offset if dx else 260)
            for walls in layouts:
                record(position, direction, walls)
        for axial in [-19.75, -.5, .5, 9.75, 19.75, 20, 39.5, 40.5]:
            for cross in [-10, -.25, 0, 10]:
                position = (220+(axial if dx else cross), 260+(cross if dx else axial))
                for walls in layouts:
                    record(position, direction, walls, forecast=False)
        blocked.clear()
        for dt in (17, 20, 25):
            for speed in (1, 2.5, 3.077):
                position = (220, 260)
                results = []
                for frame in range(50):
                    _, position = move(position, direction, dt, speed)
                    results.append(position)
                print(json.dumps(dict(kind='sequence', position=[220, 260], direction=direction,
                                      dt=dt, speed=speed, blocked=[], results=results)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('client_root', type=pathlib.Path)
    run(parser.parse_args().client_root)
