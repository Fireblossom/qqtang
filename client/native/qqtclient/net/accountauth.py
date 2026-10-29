"""Local account password login (port of internal/accountauth).

Frames are a 4-byte big-endian total length, the magic QQTAUTH1 and a message
type. The client proves knowledge of the password with a SCRAM-like proof;
the server then grants the next REQUEST_LOGIN for this UIN.
"""

from __future__ import annotations

import dataclasses
import hashlib
import hmac
import socket
import struct

MAGIC = b"QQTAUTH1"
HELLO, CHALLENGE, PROOF, RESULT = 1, 2, 3, 4
KEY_SIZE = 32
NONCE_SIZE = 32
SIGNATURE_LABEL = b"QQTang local account login\x00"


class AuthError(Exception):
    pass


@dataclasses.dataclass
class AuthResult:
    accepted: bool
    reason: str
    nickname: str = ""
    gender: int = 0


def frame(message_type: int, body: bytes) -> bytes:
    payload = MAGIC + bytes([message_type]) + body
    return struct.pack(">I", 4 + len(payload)) + payload


def client_proof(password: bytes, salt: bytes, iterations: int, nonce: bytes, uin: int) -> bytes:
    salted = hashlib.pbkdf2_hmac("sha256", password, salt, iterations, KEY_SIZE)
    client_key = hmac.new(salted, b"Client Key", hashlib.sha256).digest()
    stored_key = hashlib.sha256(client_key).digest()
    signature = hmac.new(stored_key, SIGNATURE_LABEL + struct.pack(">I", uin) + nonce, hashlib.sha256).digest()
    return bytes(a ^ b for a, b in zip(client_key, signature))


def read_frame(sock: socket.socket) -> bytes:
    header = _read_exact(sock, 4)
    (length,) = struct.unpack(">I", header)
    if length < 13 or length > 4096:
        raise AuthError(f"invalid account-auth frame length {length}")
    data = header + _read_exact(sock, length - 4)
    if data[4:12] != MAGIC:
        raise AuthError("server did not answer with an account-auth frame")
    return data


def _read_exact(sock: socket.socket, size: int) -> bytes:
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise AuthError("connection closed during account authentication")
        data += chunk
    return data


def parse_result(data: bytes) -> AuthResult:
    payload = data[13:]
    if len(payload) < 2 or len(payload) < 2 + payload[1]:
        raise AuthError("malformed account-auth result")
    reason_end = 2 + payload[1]
    result = AuthResult(payload[0] == 1, payload[2:reason_end].decode("utf-8", "replace"))
    profile = payload[reason_end:]
    if profile:
        result.gender = profile[0]
        result.nickname = profile[2:2 + profile[1]].decode("utf-8", "replace")
    return result


def authenticate(address: tuple[str, int], uin: int, password: str, timeout: float = 10.0) -> AuthResult:
    with socket.create_connection(address, timeout=timeout) as sock:
        sock.settimeout(timeout)
        sock.sendall(frame(HELLO, struct.pack(">I", uin)))
        challenge = read_frame(sock)
        if challenge[12] == RESULT:
            return parse_result(challenge)
        if challenge[12] != CHALLENGE:
            raise AuthError(f"unexpected account-auth message {challenge[12]}")
        body = challenge[13:]
        challenged_uin, iterations = struct.unpack_from(">2I", body, 0)
        salt_length = body[8]
        salt = body[9:9 + salt_length]
        nonce = body[9 + salt_length:]
        if challenged_uin != uin or len(nonce) != NONCE_SIZE:
            raise AuthError("malformed account-auth challenge")
        proof = client_proof(password.encode("ascii"), salt, iterations, nonce, uin)
        sock.sendall(frame(PROOF, struct.pack(">I", uin) + proof))
        answer = read_frame(sock)
        if answer[12] != RESULT:
            raise AuthError(f"unexpected account-auth message {answer[12]}")
        return parse_result(answer)
