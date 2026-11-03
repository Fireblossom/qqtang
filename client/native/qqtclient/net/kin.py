"""Kin (家族) messages, laid out as internal/protocol/game/kin.go encodes and
decodes them (docs/kin-re.md §3, §5). Counted strings are sent compact."""

from __future__ import annotations

import struct
import time
import types

CREATE_COMMAND = 0x00B5
FETCH_BASE_COMMAND = 0x00B6
FETCH_MEMBERS_COMMAND = 0x00B7
OPERATE_COMMAND = 0x00BA
SERVER_OPERATE_COMMAND = 0x00BC
SERVER_OPERATE_NOTIFY_COMMAND = 0x00BD
CHAT_COMMAND = 0x00BE
CHAT_NOTIFY_COMMAND = 0x00BF
KICK_COMMAND = 0x00C0
EXIT_COMMAND = 0x00C1
DISMISS_COMMAND = 0x00C2
EVENT_NOTIFY_COMMAND = 0x00C4
ASSIGN_AUTHORITY_COMMAND = 0x00C5
SET_AUTHORITY_COMMAND = 0x00C6
SET_BADGE_COMMAND = 0x00C7
FETCH_TOP_COMMAND = 0x00CB
SET_DECLARATION_COMMAND = 0x0140
SET_NOTIFICATION_COMMAND = 0x0141

NAME_SLOT = 17
DECLARATION_MAX = 257
TITLE_MAX = 200
CHAT_MAX = 0x200
CREATE_STATUS = 0x10
REJECTED = 0xFFFFFFFF
TITLE_GRADES = (12, 10, 8, 6, 4)
TOP_RECORDS = 40


def _gbk(text: str, limit: int) -> bytes:
    return text.encode("gbk", "replace")[:limit]


def _text(data: bytes) -> str:
    return data.split(b"\0", 1)[0].decode("gbk", "replace")


def _now(now: int | None) -> int:
    return (int(time.time()) if now is None else now) & 0xFFFFFFFF


def build_create(uin: int, name: str, declaration: str, section: int, now: int | None = None) -> bytes:
    content = _gbk(declaration, DECLARATION_MAX)
    return (struct.pack(">IIIHH", uin, _now(now), CREATE_STATUS, 0, len(content)) + _gbk(name, NAME_SLOT).ljust(NAME_SLOT, b"\0")
            + content + struct.pack(">I", section))


def build_words(*values: int) -> bytes:
    """Fixed u32 requests: fetch base/members, operate, kick, exit, dismiss,
    authority, badge, top (callers put the time where it belongs)."""
    return struct.pack(f">{len(values)}I", *(value & 0xFFFFFFFF for value in values))


def build_text(first: int, second: int, third: int, text: bytes) -> bytes:
    """Title (KinIndex, Uin, Time), declaration (Uin, Time, KinIndex) and
    notification (KinIndex, Uin, Time) requests: u16 length + text."""
    return struct.pack(">IIIH", first, second, third, len(text)) + text


def build_chat(uin: int, kin_index: int, text: str, now: int | None = None) -> bytes:
    message = _gbk(text, CHAT_MAX)
    return struct.pack(">IIII", uin, _now(now), kin_index, len(message)) + message


def encode_titles(names: list[str]) -> bytes:
    """QQTSection 0x10011F37: "5 " then "grade length name " per position;
    the length counts the trailing space."""
    wire = f"{len(names)} ".encode()
    for grade, name in zip(TITLE_GRADES, names):
        encoded = _gbk(name, 10)
        wire += f"{grade} {len(encoded) + 1} ".encode() + encoded + b" "
    return wire[:TITLE_MAX]


def decode_titles(wire: bytes) -> list[tuple[int, str]]:
    """The position table (0x100120C9): [(grade, name)]."""
    titles: list[tuple[int, str]] = []
    cursor = 0

    def number() -> int:
        nonlocal cursor
        end = wire.index(b" ", cursor)
        value = int(wire[cursor:end])
        cursor = end + 1
        return value

    try:
        for _ in range(number()):
            grade, length = number(), number()
            titles.append((grade, wire[cursor:cursor + length - 1].decode("gbk", "replace")))
            cursor += length
    except (ValueError, IndexError):
        pass
    return titles


class Reader:
    def __init__(self, data: bytes, offset: int = 0) -> None:
        self.data, self.offset = data, offset

    def u8(self) -> int:
        value = self.data[self.offset]
        self.offset += 1
        return value

    def u16(self) -> int:
        (value,) = struct.unpack_from(">H", self.data, self.offset)
        self.offset += 2
        return value

    def u32(self) -> int:
        (value,) = struct.unpack_from(">I", self.data, self.offset)
        self.offset += 4
        return value

    def raw(self, length: int) -> bytes:
        value = self.data[self.offset:self.offset + length]
        self.offset += length
        return value

    def text16(self) -> str:
        return _text(self.raw(self.u16()))

    def left(self) -> int:
        return len(self.data) - self.offset


def empty_base() -> types.SimpleNamespace:
    """A KINBASE with every field the scripts read (Client.exe 0x4D0380)."""
    return types.SimpleNamespace(
        m_dwKinIndex=0, m_dwUin=0, m_dwTime=0, m_dwDismissTime=0, m_dwStatus=0, m_iGrade=0,
        m_stKinFlagID=types.SimpleNamespace(m_dwIndex=0, m_iFlagID=0), m_wMemberNum=0, m_wContentLen=0, m_wTitleLen=0,
        m_szName="", m_szContent="", m_szTitle="", m_iKinSection=0, m_wNotificationLen=0, m_szNotification="",
        m_iLastHonor=0, title_wire=b"", base_update=0, list_update=0, honor=0, active_point=0, last_active_point=0,
        last_honor_order=0, last_active_order=0, before_honor_order=0, before_active_order=0)


def read_base(reader: Reader) -> types.SimpleNamespace:
    base = empty_base()
    base.m_dwKinIndex, base.m_dwUin, base.m_dwTime, base.m_dwDismissTime, base.m_dwStatus = (reader.u32() for _ in range(5))
    grade = reader.u32()
    base.m_iGrade = grade - (1 << 32) if grade & 0x80000000 else grade
    flag_index, flag_id = reader.u32(), reader.u32()
    base.m_stKinFlagID = types.SimpleNamespace(m_dwIndex=flag_index, m_iFlagID=flag_id - (1 << 32) if flag_id & 0x80000000 else flag_id)
    base.m_wMemberNum, base.m_wContentLen, base.m_wTitleLen = reader.u16(), reader.u16(), reader.u16()
    base.m_szName = _text(reader.raw(NAME_SLOT))
    base.m_szContent = _text(reader.raw(base.m_wContentLen))
    base.title_wire = reader.raw(base.m_wTitleLen)
    base.m_szTitle = base.title_wire.decode("gbk", "replace")
    base.m_iKinSection, base.base_update, base.list_update = reader.u32(), reader.u32(), reader.u32()
    base.m_wNotificationLen = reader.u16()
    base.m_szNotification = _text(reader.raw(base.m_wNotificationLen))
    (base.honor, base.active_point, base.m_iLastHonor, base.last_active_point, base.last_honor_order, base.last_active_order,
     base.before_honor_order, base.before_active_order) = (reader.u32() for _ in range(8))
    return base


def read_members(reader: Reader) -> tuple[int, int, list[dict]]:
    """0xB7 after Uin: ListUpdate, Result, Num, MemList, MemAttachList."""
    list_update, result, count = reader.u32(), reader.u16(), reader.u16()
    members = []
    for _ in range(count):
        uin = reader.u32()
        nickname = reader.text16()
        _joined, status, _status_time, grade, _online, _last = (reader.u32() for _ in range(6))
        members.append({"uin": uin, "nickname": nickname, "status": status, "grade": grade, "honor": 0, "active": 0})
    for member in members:
        if reader.left() >= 8:
            member["honor"], member["active"] = reader.u32(), reader.u32()
    return list_update, result, members


def read_top(reader: Reader) -> dict:
    """0xCB after Result: OrderTime, Uin, TopKinInfo. The two SameGroup
    records may be missing (the Go server writes none)."""
    reader.u32()
    reader.u32()
    order_time = reader.u32()
    records = []
    for _ in range(TOP_RECORDS):
        kin_index = reader.u32()
        name = _text(reader.raw(reader.u8()))
        value, order, before = reader.u32(), reader.u16(), reader.u16()
        records.append({"kin": kin_index, "name": name, "value": value, "order": order, "before": before})
    group_num = reader.u16() if reader.left() >= 2 else 0
    group_members = reader.u16() if reader.left() >= 2 else 0
    for _ in range(2):
        if reader.left() < 13:
            records.append({"kin": 0, "name": "", "value": 0, "order": 0, "before": 0})
            continue
        kin_index = reader.u32()
        name = _text(reader.raw(reader.u8()))
        records.append({"kin": kin_index, "name": name, "value": reader.u32(), "order": reader.u16(), "before": reader.u16()})
    return {"order_time": order_time, "records": records, "group_num": group_num, "group_members": group_members}
