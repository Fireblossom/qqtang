"""Game-service TCP connection (see internal/protocol/game/local_packet.go).

Every packet is:
  0   u32  total length (including these four bytes)
  4   u32  outer sequence
  8   u16  route sequence
  10  u16  0xFFFF
  12  u32  envelope UIN
  16  u8   1, u8 32 (key/ST marker), then the 32-byte local ST
  50  QQ-TEA(LocalKey) of: u16 command, u32 flags, u16 inner sequence,
      u16 route, u16 marker (0xFFFF), u16 section ID, payload
All integers are big-endian.
"""

from __future__ import annotations

import dataclasses
import socket
import struct
import threading
from typing import Callable

from . import qqtea

LOCAL_KEY = bytes(range(0x10, 0x20))
LOCAL_ST = bytes(range(0x20, 0x40))
ENVELOPE_SIZE = 18
ENCRYPTED_OFFSET = ENVELOPE_SIZE + len(LOCAL_ST)
INNER_HEADER_SIZE = 14
MAX_PACKET = 1 << 20

# Routes of the inner header: the directory, and the player/lobby subsystem
# (captured room-list request: 0066 00000000 0012 0002 ffff 0001 ...).
ROUTE_DIRECTORY = 6
ROUTE_PLAYER = 2
MARKER = 0xFFFF
DIRECTORY_SECTION = 0x00FD

DIRECTORY_COMMAND = 0x0133
LOGIN_COMMAND = 0x0064


@dataclasses.dataclass
class Packet:
    outer_sequence: int
    route_sequence: int
    uin: int
    command: int
    flags: int
    inner_sequence: int
    route: int
    marker: int
    section_id: int
    payload: bytes


def encode(packet: Packet) -> bytes:
    inner = struct.pack(">HIHHHH", packet.command, packet.flags, packet.inner_sequence, packet.route, packet.marker, packet.section_id)
    ciphertext = qqtea.encrypt(inner + packet.payload, LOCAL_KEY)
    header = struct.pack(">IIHHIBB", ENCRYPTED_OFFSET + len(ciphertext), packet.outer_sequence, packet.route_sequence, 0xFFFF, packet.uin, 1, len(LOCAL_ST))
    return header + LOCAL_ST + ciphertext


def decode(data: bytes) -> Packet:
    if len(data) < ENCRYPTED_OFFSET + 16:
        raise ValueError(f"game packet length {len(data)} is too short")
    total, outer_sequence, route_sequence, _, uin, marker, st_size = struct.unpack_from(">IIHHIBB", data, 0)
    if total != len(data) or marker != 1 or st_size != len(LOCAL_ST):
        raise ValueError("unsupported game packet envelope")
    plaintext = qqtea.decrypt(data[ENCRYPTED_OFFSET:], LOCAL_KEY)
    if len(plaintext) < INNER_HEADER_SIZE:
        raise ValueError("game packet plaintext is too short")
    command, flags, inner_sequence, route, inner_marker, section_id = struct.unpack_from(">HIHHHH", plaintext, 0)
    return Packet(outer_sequence, route_sequence, uin, command, flags, inner_sequence, route, inner_marker, section_id, plaintext[INNER_HEADER_SIZE:])


def read_packet(sock: socket.socket) -> bytes | None:
    header = _read_exact(sock, 4)
    if header is None:
        return None
    (length,) = struct.unpack(">I", header)
    if not ENCRYPTED_OFFSET + 16 <= length <= MAX_PACKET:
        raise ValueError(f"invalid game packet length {length}")
    body = _read_exact(sock, length - 4)
    if body is None:
        raise ConnectionError("connection closed inside a packet")
    return header + body


def _read_exact(sock: socket.socket, size: int) -> bytes | None:
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            if data:
                raise ConnectionError("connection closed inside a packet")
            return None
        data += chunk
    return data


def request_directory(address: tuple[str, int], uin: int, timeout: float = 10.0) -> bytes:
    """Send the client's 0x0133 discovery request and return the hall payload."""
    request = Packet(1, 1, uin, DIRECTORY_COMMAND, 0, 1, ROUTE_DIRECTORY, MARKER, DIRECTORY_SECTION, b"")
    with socket.create_connection(address, timeout=timeout) as sock:
        sock.settimeout(timeout)
        sock.sendall(encode(request))
        data = read_packet(sock)
    if data is None:
        raise ConnectionError("directory server closed the connection")
    response = decode(data)
    if response.command != DIRECTORY_COMMAND:
        raise ValueError(f"unexpected directory response command 0x{response.command:04X}")
    return response.payload


class GameConnection:
    """A long-lived game-service connection with a background reader thread."""

    def __init__(self, address: tuple[str, int], uin: int, section_id: int,
                 on_packet: Callable[[Packet], None], on_closed: Callable[[str], None], timeout: float = 10.0):
        self.uin = uin
        self.section_id = section_id
        self.on_packet = on_packet
        self.on_closed = on_closed
        self.sock = socket.create_connection(address, timeout=timeout)
        self.sock.settimeout(None)
        self.lock = threading.Lock()
        self.outer_sequence = 0
        self.inner_sequence = 0
        self.closed = False
        self.reader = threading.Thread(target=self._read_loop, name="game-connection", daemon=True)
        self.reader.start()

    def send(self, command: int, payload: bytes, route: int = ROUTE_PLAYER) -> None:
        with self.lock:
            self.outer_sequence += 1
            self.inner_sequence = (self.inner_sequence + 1) & 0xFFFF
            packet = Packet(self.outer_sequence, self.outer_sequence & 0xFFFF, self.uin, command, 0,
                            self.inner_sequence, route, MARKER, self.section_id, payload)
            self.sock.sendall(encode(packet))

    def close(self) -> None:
        self.closed = True
        try:
            self.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.sock.close()

    def _read_loop(self) -> None:
        reason = "server closed the connection"
        try:
            while True:
                data = read_packet(self.sock)
                if data is None:
                    break
                self.on_packet(decode(data))
        except (OSError, ValueError) as error:
            reason = str(error)
        if not self.closed:
            self.on_closed(reason)
