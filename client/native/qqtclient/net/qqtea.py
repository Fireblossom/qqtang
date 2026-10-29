"""Legacy Tencent QQ-TEA chaining (port of internal/protocol/qqtea/qqtea.go)."""

from __future__ import annotations

import os
import struct

BLOCK = 8
DELTA = 0x9E3779B9
ROUNDS = 16
MASK = 0xFFFFFFFF


def _encrypt_block(block: bytes, key: tuple[int, int, int, int]) -> bytes:
    left, right = struct.unpack(">2I", block)
    k0, k1, k2, k3 = key
    total = 0
    for _ in range(ROUNDS):
        total = (total + DELTA) & MASK
        left = (left + ((((right << 4) + k0) & MASK) ^ ((right + total) & MASK) ^ (((right >> 5) + k1) & MASK))) & MASK
        right = (right + ((((left << 4) + k2) & MASK) ^ ((left + total) & MASK) ^ (((left >> 5) + k3) & MASK))) & MASK
    return struct.pack(">2I", left, right)


def _decrypt_block(block: bytes, key: tuple[int, int, int, int]) -> bytes:
    left, right = struct.unpack(">2I", block)
    k0, k1, k2, k3 = key
    total = (DELTA * ROUNDS) & MASK
    for _ in range(ROUNDS):
        right = (right - ((((left << 4) + k2) & MASK) ^ ((left + total) & MASK) ^ (((left >> 5) + k3) & MASK))) & MASK
        left = (left - ((((right << 4) + k0) & MASK) ^ ((right + total) & MASK) ^ (((right >> 5) + k1) & MASK))) & MASK
        total = (total - DELTA) & MASK
    return struct.pack(">2I", left, right)


def _key(key: bytes) -> tuple[int, int, int, int]:
    if len(key) != 16:
        raise ValueError(f"QQ-TEA key length = {len(key)}, want 16")
    return struct.unpack(">4I", key)


def _xor(a: bytes, b: bytes) -> bytes:
    return bytes(x ^ y for x, y in zip(a, b))


def encrypt(plaintext: bytes, key: bytes, entropy: bytes | None = None) -> bytes:
    words = _key(key)
    padding = (BLOCK - ((len(plaintext) + 10) % BLOCK)) % BLOCK
    prefix = entropy if entropy is not None else os.urandom(padding + 3)
    formatted = bytes([prefix[0] & 0xF8 | padding]) + prefix[1:padding + 3] + plaintext + bytes(7)
    output = bytearray()
    previous_cipher = bytes(BLOCK)
    previous_intermediate = bytes(BLOCK)
    for offset in range(0, len(formatted), BLOCK):
        intermediate = _xor(formatted[offset:offset + BLOCK], previous_cipher)
        cipher = _xor(_encrypt_block(intermediate, words), previous_intermediate)
        output += cipher
        previous_intermediate, previous_cipher = intermediate, cipher
    return bytes(output)


def decrypt(ciphertext: bytes, key: bytes) -> bytes:
    words = _key(key)
    if len(ciphertext) < 16 or len(ciphertext) % BLOCK:
        raise ValueError(f"QQ-TEA ciphertext length = {len(ciphertext)}")
    formatted = bytearray()
    previous_cipher = bytes(BLOCK)
    previous_plain = bytes(BLOCK)
    for offset in range(0, len(ciphertext), BLOCK):
        block = ciphertext[offset:offset + BLOCK]
        plain = _decrypt_block(_xor(block, previous_plain), words)
        formatted += _xor(plain, previous_cipher)
        previous_plain, previous_cipher = plain, block
    padding = formatted[0] & 0x07
    start, end = 1 + padding + 2, len(formatted) - 7
    if start > end or any(formatted[end:]):
        raise ValueError("QQ-TEA padding validation failed")
    return bytes(formatted[start:end])
