"""Craft/egg messages on the live game connection; see docs/craft-re.md."""

from __future__ import annotations

import dataclasses
import struct

from .shop import _stamp

COMBINE_COMMAND = 0x00CF
FORGE_COMMAND = 0x0353
BREAK_EGG_COMMAND = 0x00FE


def build_combine(uin: int, product: int, materials: list[int], now: int | None = None) -> bytes:
    return (_stamp(uin, now) + struct.pack(">BIIH", 0, product, 0, len(materials))
            + b"".join(struct.pack(">I", item) for item in materials))


def build_forge(uin: int, item: int, material: int, operation: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">BII", operation, item, material)


def build_break_egg(uin: int, egg: int, hammer: int, now: int | None = None) -> bytes:
    # Script arguments are egg, hammer; the wire puts the hammer first.
    return _stamp(uin, now) + struct.pack(">II", hammer, egg)


def counted_text(payload: bytes, offset: int, length: int) -> str:
    if offset + length != len(payload):
        raise ValueError("invalid craft text length")
    return payload[offset:].split(b"\0", 1)[0].decode("gbk", "replace")


@dataclasses.dataclass
class CombineResult:
    result: int
    operation: int
    item: int
    reason: str


def parse_combine(payload: bytes) -> CombineResult:
    if len(payload) < 15:
        raise ValueError("short combine result")
    result, operation, item, _source, count = struct.unpack_from(">HBIIH", payload)
    if count > 20 or len(payload) < 15 + count * 4:
        raise ValueError("invalid combine material count")
    offset = 13 + count * 4
    length = struct.unpack_from(">H", payload, offset)[0]
    return CombineResult(result, operation, item, counted_text(payload, offset + 2, length))


@dataclasses.dataclass
class ForgeResult:
    result: int
    operation: int
    item: int
    material: int
    effect: int
    color: int
    reason: str


def parse_forge(payload: bytes) -> ForgeResult:
    if len(payload) < 15:
        raise ValueError("short forge result")
    result, operation, item, material, effect, color, length = struct.unpack_from(">HBIIBBH", payload)
    return ForgeResult(result, operation, item, material, effect, color, counted_text(payload, 15, length))


def parse_break_egg(payload: bytes) -> tuple[int, int, str]:
    if len(payload) < 7:
        raise ValueError("short break-egg result")
    result, item, length = struct.unpack_from(">HIB", payload)
    return result, item, counted_text(payload, 7, length)
