"""QQF/DIMG sprite decoding (port of internal/game/itemcatalog/dimg.go)."""

from __future__ import annotations

import dataclasses
import struct

import numpy as np

MAGIC = b"QQF\x1aDIMG"
SPRITE_HEADER_SIZE = 40
FRAME_HEADER_SIZE = 16
IMAGE_HEADER_SIZE = 12
MAX_DIMENSION = 4096
MAX_FRAMES = 1024


@dataclasses.dataclass
class Frame:
    x: int
    y: int
    # Height x width x 4 RGBA pixels; None for an empty frame.
    pixels: np.ndarray | None


@dataclasses.dataclass
class Sprite:
    width: int
    height: int
    directions: int
    frames: list[Frame]


class DIMGError(ValueError):
    pass


def parse(data: bytes) -> Sprite:
    if len(data) < SPRITE_HEADER_SIZE or not data.startswith(MAGIC):
        raise DIMGError("not a QQF/DIMG resource")
    version, frame_info_size, frame_count, directions = struct.unpack_from("<4I", data, 8)
    width, height = struct.unpack_from("<2I", data, 32)
    if version not in (65536, 65537):
        raise DIMGError(f"QQF/DIMG version {version} is unsupported")
    if frame_info_size != 24:
        raise DIMGError(f"QQF/DIMG frame-info size {frame_info_size} is unsupported")
    if not (1 <= frame_count <= MAX_FRAMES and 1 <= directions <= 32 and frame_count % directions == 0):
        raise DIMGError(f"invalid QQF/DIMG frame/direction count {frame_count}/{directions}")
    if not (1 <= width <= MAX_DIMENSION and 1 <= height <= MAX_DIMENSION):
        raise DIMGError(f"invalid QQF/DIMG canvas {width}x{height}")

    offset = SPRITE_HEADER_SIZE
    frames: list[Frame] = []
    for index in range(frame_count):
        if len(data) - offset < FRAME_HEADER_SIZE:
            raise DIMGError(f"QQF/DIMG frame {index} header is truncated")
        magic, x, y, mode = struct.unpack_from("<IiiI", data, offset)
        if magic != 0:
            raise DIMGError(f"QQF/DIMG frame {index} magic {magic:#x} is unsupported")
        offset += FRAME_HEADER_SIZE
        if mode == 0:
            frames.append(Frame(x, y, None))
            continue
        if len(data) - offset < IMAGE_HEADER_SIZE:
            raise DIMGError(f"QQF/DIMG frame {index} image header is truncated")
        frame_width, frame_height = struct.unpack_from("<2I", data, offset)
        offset += IMAGE_HEADER_SIZE
        # Absent directions may use a non-zero mode with a 0x0 image header.
        if frame_width == 0 and frame_height == 0:
            frames.append(Frame(x, y, None))
            continue
        if not (1 <= frame_width <= MAX_DIMENSION and 1 <= frame_height <= MAX_DIMENSION):
            raise DIMGError(f"invalid QQF/DIMG frame {index} size {frame_width}x{frame_height}")
        if mode in (3, 0x11000000, 16):
            bytes_per_pixel = 3
        elif mode == 8:
            bytes_per_pixel = 4
        else:
            raise DIMGError(f"QQF/DIMG frame {index} pixel mode {mode:#x} is unsupported")
        byte_count = frame_width * frame_height * bytes_per_pixel
        if len(data) - offset < byte_count:
            raise DIMGError(f"QQF/DIMG frame {index} pixel data is truncated")
        pixels = _decode_pixels(data[offset:offset + byte_count], mode, frame_width, frame_height)
        offset += byte_count
        frames.append(Frame(x, y, pixels))
    # Version 65537 may carry a container suffix after the declared frames.
    if offset != len(data) and version != 65537:
        raise DIMGError(f"QQF/DIMG has {len(data) - offset} trailing bytes")
    return Sprite(width, height, directions, frames)


def _decode_pixels(data: bytes, mode: int, width: int, height: int) -> np.ndarray:
    count = width * height
    raw = np.frombuffer(data, dtype=np.uint8)
    rgba = np.empty((count, 4), dtype=np.uint8)
    if mode in (3, 0x11000000):
        # RGB565 plane followed by a 5-bit alpha plane (0..32).
        value = raw[:count * 2].view("<u2").astype(np.uint32)
        rgba[:, 0] = ((value >> 11) & 0x1F) * 255 // 31
        rgba[:, 1] = ((value >> 5) & 0x3F) * 255 // 63
        rgba[:, 2] = (value & 0x1F) * 255 // 31
        alpha = np.minimum(raw[count * 2:count * 3].astype(np.uint32), 32)
        rgba[:, 3] = (alpha * 255 + 16) // 32
    elif mode == 8:
        bgra = raw.reshape(count, 4)
        rgba[:, 0], rgba[:, 1], rgba[:, 2], rgba[:, 3] = bgra[:, 2], bgra[:, 1], bgra[:, 0], bgra[:, 3]
    else:
        bgr = raw.reshape(count, 3)
        rgba[:, 0], rgba[:, 1], rgba[:, 2] = bgr[:, 2], bgr[:, 1], bgr[:, 0]
        rgba[:, 3] = 255
    return rgba.reshape(height, width, 4)
