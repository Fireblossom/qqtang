"""Pet messages (docs/pet-re.md §4, §5), laid out as
internal/protocol/game/pet.go encodes and decodes them. A PET_BASE_INFO goes
over the wire compact: 38 bytes up to SkillCount, then one byte per skill."""

from __future__ import annotations

import struct
import time
from dataclasses import dataclass

PLAYER_PETS_COMMAND = 0x00D4
HANDLE_PET_COMMAND = 0x00D5
ROUTE = 4  # the QQTSection header {uin, 1, 4, ...} (0x10030DF5)

EVENT_FEED, EVENT_TAKE, EVENT_STOP_TAKE, EVENT_FREE, EVENT_RENAME, EVENT_LEARN, EVENT_ADOPT = range(1, 8)
STATE_ACTIVE, STATE_RESTING = 1, 2
BASE_CAPACITY, EXTRA_CAPACITY = 5, 20
NAME_SLOT, SKILL_SLOT, PARAMETER_SLOT = 12, 20, 20
PET_MINIMUM = 38

# RaisePet's item ranges (Client.exe 0x550080).
FOOD_ITEMS = range(26001, 27001)
SKILL_ITEMS = range(27001, 28001)
CARD_ITEMS = range(28001, 29001)
PET_ITEMS = range(26001, 29001)


@dataclass
class Pet:
    pet_id: int = 0
    type_id: int = 0
    experience: int = 0
    loyalty: int = 0
    level: int = 0
    mood: int = 0
    state: int = 0
    name: str = ""
    skills: bytes = b""


def parse_pet(data: bytes, offset: int = 0) -> tuple[Pet, int]:
    """One compact pet and the offset after it."""
    pet_id, type_id, experience, loyalty, level, mood, state = struct.unpack_from(">IIIIHHH", data, offset)
    raw_name = data[offset + 22:offset + 34]
    count = struct.unpack_from(">I", data, offset + 34)[0]
    end = offset + PET_MINIMUM + count
    if end > len(data):
        raise ValueError(f"pet skills run past the packet: {count}")
    name = raw_name.split(b"\0", 1)[0].decode("gbk", "replace")
    return Pet(pet_id, type_id, experience, loyalty, level, mood, state, name, bytes(data[offset + PET_MINIMUM:end])), end


def parse_pets_info(data: bytes) -> tuple[list[Pet], list[Pet]]:
    offset = 0
    groups: list[list[Pet]] = []
    for _ in range(2):
        count = struct.unpack_from(">b", data, offset)[0]
        offset += 1
        pets = []
        for _ in range(max(count, 0)):
            pet, offset = parse_pet(data, offset)
            pets.append(pet)
        groups.append(pets)
    return groups[0], groups[1]


def parse_handle_pet(data: bytes) -> tuple[int, int, str, Pet | None]:
    """(result, event, reason, pet). The pet is None when it does not decode;
    Go appends a zero record on failure, which the original never reads."""
    result, event, length = struct.unpack_from(">hIH", data, 0)
    reason_bytes = data[8:8 + length]
    reason = reason_bytes.split(b"\0", 1)[0].decode("gbk", "replace") if 0 <= length <= 200 else ""
    try:
        pet, _ = parse_pet(data, 8 + length)
    except (struct.error, ValueError):
        pet = None
    return result, event, reason, pet


def now() -> int:
    return int(time.time()) & 0xFFFFFFFF


def build_pets_request(uin: int) -> bytes:
    return struct.pack(">II", uin, now())


def gbk_prefix(text: str, limit: int) -> bytes:
    """GBK bytes of text, cut before the character that would pass limit."""
    out = b""
    for char in text:
        encoded = char.encode("gbk", "replace")
        if len(out) + len(encoded) > limit:
            break
        out += encoded
    return out


def build_handle_pet(uin: int, event: int, pet_id: int, parameter: str = "") -> bytes:
    """REQUEST_HANDLE_PET: ParaBuf is sprintf'd into 20 bytes; keep 19 and the
    terminator."""
    return struct.pack(">IIII", uin, now(), event, pet_id) + gbk_prefix(parameter, PARAMETER_SLOT - 1).ljust(PARAMETER_SLOT, b"\0")
