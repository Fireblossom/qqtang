#!/usr/bin/env python3
"""Default the original Python 2.3 shop to the sugar-currency catalog."""

import argparse
import importlib.util
from pathlib import Path
import struct
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--client-root', required=True, type=Path)
    parser.add_argument('--xdis-root', required=True, type=Path)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    sys.path.insert(0, str(args.xdis_root))
    from xdis.load import load_module
    from xdis.bytecode import Bytecode
    from xdis.op_imports import get_opcode_module

    spec = importlib.util.spec_from_file_location('registry_patch', Path(__file__).with_name('patch-python23-item-registry.py'))
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    path = args.client_root / 'res/uiRes/uiShop.pyc'
    version, _, _, module, *_ = load_module(str(path))
    if version != (2, 3):
        raise RuntimeError('shop patch requires Python 2.3 bytecode')
    opcode = get_opcode_module(version)

    def find_init(code):
        if code.co_name == 'OnInit' and 'curMoneyType' in code.co_names:
            yield code
        for value in code.co_consts:
            if hasattr(value, 'co_code'):
                yield from find_init(value)

    init, = find_init(module)
    instructions = list(Bytecode(init, opcode))
    assignments = [instructions[i - 1] for i, instruction in enumerate(instructions)
                   if instruction.opname == 'STORE_GLOBAL' and instruction.argval == 'curMoneyType']
    if len(assignments) != 2 or any(x.opname != 'LOAD_CONST' for x in assignments):
        raise RuntimeError('unexpected shop currency initialization')
    if assignments[1].argval == 'TangB':
        print('shop default verified: sugar currency')
        return
    if assignments[1].argval != 'QB' or args.check:
        raise RuntimeError('shop does not default to sugar currency')

    original = path.read_bytes()
    marker = helper.marshal_value(init.co_code)
    if original.count(marker) != 1:
        raise RuntimeError('shop initializer bytecode is not unique')
    start = original.index(marker)
    constants = start + len(marker)
    scanner = helper.Marshal23Scanner(original)
    end = scanner.skip(constants)
    count = scanner.int32(constants + 1)
    if original[constants:constants + 1] != b'(' or count != len(init.co_consts):
        raise RuntimeError('unexpected shop initializer constants')
    code = bytearray(init.co_code)
    struct.pack_into('<H', code, assignments[1].offset + 1, count)
    rewritten = (original[:start] + helper.marshal_value(bytes(code)) + b'('
                 + struct.pack('<i', count + 1) + original[constants + 5:end]
                 + helper.marshal_value(b'TangB') + original[end:])
    temporary = path.with_suffix('.pyc.tmp')
    try:
        temporary.write_bytes(rewritten)
        _, _, _, verified, *_ = load_module(str(temporary))
        new_init, = find_init(verified)
        assert new_init.co_consts[-1] == 'TangB'
        assert new_init.co_code == bytes(code)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)
    print('shop default patched: sugar currency')


if __name__ == '__main__':
    main()
