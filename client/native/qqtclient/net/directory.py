"""Directory RESPONSE_HALL_INFO (schema 0x0816) parsing.

Layout mirrors internal/protocol/directory/hall_payload.go; all integers are
big-endian and names are GBK.
"""

from __future__ import annotations

import dataclasses
import ipaddress
import struct


@dataclasses.dataclass
class Location:
    id: int
    name: str


@dataclasses.dataclass
class Server:
    id: int
    ip: str
    port: int
    udp_port: int


@dataclasses.dataclass
class Section:
    name: str
    id: int
    server_id: int
    max_players: int
    current_players: int
    location_id: int


@dataclasses.dataclass
class Channel:
    name: str
    id: int
    sections: list[Section]


@dataclasses.dataclass
class HallInfo:
    version: int
    build: int
    locations: list[Location]
    servers: list[Server]
    channels: list[Channel]

    def server(self, server_id: int) -> Server | None:
        return next((server for server in self.servers if server.id == server_id), None)


class Reader:
    def __init__(self, data: bytes):
        self.data = data
        self.offset = 0

    def take(self, size: int) -> bytes:
        if self.offset + size > len(self.data):
            raise ValueError(f"hall payload truncated at {self.offset}+{size} of {len(self.data)}")
        value = self.data[self.offset:self.offset + size]
        self.offset += size
        return value

    def u8(self) -> int:
        return self.take(1)[0]

    def u16(self) -> int:
        return struct.unpack(">H", self.take(2))[0]

    def u32(self) -> int:
        return struct.unpack(">I", self.take(4))[0]

    def name(self, size: int) -> str:
        return self.take(size).split(b"\0", 1)[0].decode("gbk", "replace")


def parse_hall(payload: bytes) -> HallInfo:
    reader = Reader(payload)
    result = reader.u16()
    if result != 0:
        raise ValueError(f"directory result {result}")
    version, build = reader.u32(), reader.u32()
    locations = []
    for _ in range(reader.u8()):
        location_id = reader.u16()
        locations.append(Location(location_id, reader.name(reader.u8())))
    reader.take(4)
    servers = []
    for _ in range(reader.u8()):
        server_id = reader.u32()
        # The address DWORD is serialized numerically from in_addr byte order.
        ip = str(ipaddress.IPv4Address(struct.pack("<I", reader.u32())))
        servers.append(Server(server_id, ip, reader.u16(), reader.u16()))
    channels = []
    for _ in range(reader.u8()):
        channel_name = reader.name(20)
        channel_id = reader.u32()
        sections = []
        for _ in range(reader.u16()):
            section_name = reader.name(reader.u8())
            section_id, server_id = reader.u16(), reader.u32()
            max_players, current_players = reader.u16(), reader.u16()
            reader.take(8)  # LowPoint, HighPoint
            location_id = reader.u16()
            reader.take(13)
            sections.append(Section(section_name, section_id, server_id, max_players, current_players, location_id))
        channels.append(Channel(channel_name, channel_id, sections))
    return HallInfo(version, build, locations, servers, channels)
