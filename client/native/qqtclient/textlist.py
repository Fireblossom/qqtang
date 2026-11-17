"""TEXTLIST lines: the "/<...>" markup, row layout and the name/link hit test.

gameUI.dll keeps each list line as a vector of 0x50-byte elements (+0 type,
+4 value, +8 width, +0xC extra, +0x10 the source text). The constructor
0x1002289D turns every byte into a character element (0x10022C0C: type 0
single byte, 3 then 1 for a double-byte character) and 0x10022CA3 then
replaces each "/" whose previous element is not a plain "/" (0x10025B3F) by
what the parser 0x100231AA recognises in the next 64 bytes:

- ``/<name=N:uin=U>`` (type 10): a zero-width marker. The next N GBK bytes
  are the name and are not parsed again, so a "/" in a nickname stays text.
- ``/<kin=K>`` (type 0xB) and ``/<level=P>`` (type 0xC): 20-pixel icons, the
  kin totem object/ui/kinTotem/%03d.img (none when K < 0, the width stays)
  and the images the scripts' ui_GetLevelMainPath/ui_GetLevelSubPath(P) name.
- ``/<H>`` and ``/<~H>`` (either case, type 8, value 8 and 0): zero-width
  link start and end.
- Colours (``/<c=RRGGBB>``, ``/<~c>``), ``/<img=..>`` and ``/<eft=..>`` are
  parsed too, but 0x1002350A only keeps them when the line's flag byte
  (+0x10) allows; 0x10022938 sets it to 0x10 (links only), so in a TEXTLIST
  they fall through to the face lookup and stay text.
- Anything else at a "/" is looked up as a face code (first match in list
  order, 0x10018F2D).

0x100236F0 lays the elements out: a double-byte character is textsize wide,
a single byte is measured (the host uses the 宋体 half width), faces use
their own advance, kin and level icons their stored 20 pixels and markers
nothing. A row breaks before the element that would pass the text width.

Clicks (0x10024190 left press, 0x1002495A right press, 0x10024D10 double
click) find the row under the mouse and the element whose [x, x + width]
holds it. A link is found by scanning back for the nearest type-8 marker
(a start makes the click a link up to the next end marker); a name by
scanning back for the nearest name marker (0x10023966) and accepting the
element when the bytes from the marker up to it are at most N.
"""

from __future__ import annotations

import dataclasses
from typing import Callable

CHAR, FACE, NAME, KIN, LEVEL, LINK = "char", "face", "name", "kin", "level", "link"
ICON_WIDTH = 20  # 0x100231AA: kin and level elements are 0x14 x 0xC
TAG_WINDOW = 64  # 0x10022CA3 copies 0x40 bytes for the parser
KIN_TOTEM = "object/ui/kinTotem/%03d.img"  # 0x10047160
HTTP = "http://"  # 0x1004667C
SAID = "说:"  # QQTSection 0x1005EA88


@dataclasses.dataclass
class Element:
    kind: str
    text: str  # the source text, which byte counts and link text include
    value: int = 0
    length: int = 0  # a name's byte count

    def byte_length(self) -> int:
        return len(self.text.encode("gbk", "replace"))


@dataclasses.dataclass
class Row:
    """One drawn row: its line's elements and its cells (index, x, width)."""
    elements: list[Element]
    cells: list[tuple[int, int, int]]


def _digits(text: str, start: int, end_char: str) -> tuple[int, int] | None:
    """Decimal digits from start up to end_char: (value, index of end_char)."""
    index, value = start, 0
    while index < len(text) and text[index].isdigit():
        value = value * 10 + int(text[index])
        index += 1
    if index >= len(text) or text[index] != end_char:
        return None
    return value, index


def _atoi(text: str) -> int:
    """C atoi: optional sign, then digits up to the first other character."""
    index, sign = 0, 1
    if text.startswith(("+", "-")):
        sign = -1 if text[0] == "-" else 1
        index = 1
    value = 0
    while index < len(text) and text[index].isdigit():
        value = value * 10 + int(text[index])
        index += 1
    return sign * value


def parse_tag(text: str, start: int) -> Element | None:
    """The tags 0x100231AA recognises at text[start] == "/" that a TEXTLIST
    keeps (names, kin, level, link start and end)."""
    window = text[start:start + TAG_WINDOW]
    if window[1:2] != "<" or len(window) < 4:
        return None
    key = window[2]
    if key in "Hh" and window[3] == ">":
        return Element(LINK, window[:4], 1)
    if key == "~" and window[3:4] in ("H", "h") and window[4:5] == ">":
        return Element(LINK, window[:5], 0)
    if window.startswith("kin=", 2) or window.startswith("level=", 2):
        number_at = 6 if key == "k" else 8
        close = window.find(">", number_at)
        if close < 0:
            return None
        return Element(KIN if key == "k" else LEVEL, window[:close + 1], _atoi(window[number_at:close]))
    if window.startswith("name=", 2):
        length = _digits(window, 7, ":")
        if length is None or not window.startswith("uin=", length[1] + 1):
            return None
        uin = _digits(window, length[1] + 5, ">")
        if uin is None:
            return None
        return Element(NAME, window[:uin[1] + 1], uin[0], length[0])
    return None


def parse(text: str, face_codes: list[str] = ()) -> list[Element]:
    """One list line as elements; "\\n" stays a character (a forced break)."""
    elements: list[Element] = []
    index = 0
    while index < len(text):
        char = text[index]
        previous = elements[-1] if elements else None
        if char == "/" and not (previous is not None and previous.kind == CHAR and previous.text == "/"):
            tag = parse_tag(text, index)
            if tag is not None:
                elements.append(tag)
                index += len(tag.text)
                if tag.kind == NAME:
                    # The name's bytes are kept as plain characters.
                    used = 0
                    while used < tag.length and index < len(text):
                        elements.append(Element(CHAR, text[index]))
                        used += elements[-1].byte_length()
                        index += 1
                continue
            face = next((i for i, code in enumerate(face_codes) if text.startswith(code, index)), None)
            if face is not None:
                elements.append(Element(FACE, face_codes[face], face))
                index += len(face_codes[face])
                continue
        elements.append(Element(CHAR, char))
        index += 1
    return elements


def element_width(element: Element, size: int, face_width: Callable[[int], int]) -> int:
    if element.kind == CHAR:
        return len(element.text.encode("gbk", "replace")) * (size // 2)
    if element.kind == FACE:
        return face_width(element.value)
    if element.kind in (KIN, LEVEL):
        return ICON_WIDTH
    return 0


def layout(elements: list[Element], size: int, width: int,
           face_width: Callable[[int], int]) -> list[list[tuple[int, int, int]]]:
    """Rows of (element index, x, width); a row breaks before the element
    that would pass width (0x100236F0) and at "\\n"."""
    rows: list[list[tuple[int, int, int]]] = [[]]
    used = 0
    for index, element in enumerate(elements):
        if element.kind == CHAR and element.text == "\n":
            rows.append([])
            used = 0
            continue
        advance = element_width(element, size, face_width)
        if width > 0 and rows[-1] and used + advance > width:
            rows.append([])
            used = 0
        rows[-1].append((index, used, advance))
        used += advance
    return rows


def element_under(rows: list[tuple[int, int, int, Row]], position: tuple[int, int]) -> tuple[Row, int] | None:
    """rows: (left, top, height, row) as drawn. The first row whose
    [top, top + height] holds the point and has an element whose
    [x, x + width] holds it (0x10024190); None when there is none."""
    px, py = position
    for left, top, height, row in rows:
        if not top <= py <= top + height:
            continue
        for index, x, advance in row.cells:
            if left + x <= px <= left + x + advance:
                return row, index
    return None


def name_at(elements: list[Element], index: int) -> int | None:
    """0x10023966: the nearest name marker at or before index whose name
    covers it, or None."""
    for marker in range(index, -1, -1):
        if elements[marker].kind == NAME:
            covered = sum(element.byte_length() for element in elements[marker + 1:index + 1])
            return marker if covered <= elements[marker].length else None
    return None


def name_text(elements: list[Element], marker: int) -> str:
    """0x10023A0D: the N bytes after the marker."""
    data = "".join(element.text for element in elements[marker + 1:]).encode("gbk", "replace")
    return data[:elements[marker].length].decode("gbk", "replace")


def link_at(elements: list[Element], index: int) -> str | None:
    """0x10024190: the link text when the nearest link marker at or before
    index is a start, up to the next end marker or the end of the line."""
    for start in range(index, -1, -1):
        element = elements[start]
        if element.kind != LINK:
            continue
        if element.value <= 0:
            return None
        end = next((i for i in range(start + 1, len(elements)) if elements[i].kind == LINK and elements[i].value <= 0),
                   len(elements))
        url = "".join(item.text for item in elements[start + 1:end])
        # The original compares the whole lowered text with "http://"
        # (basic_string::compare(0, size, "http://", 7)), so it prefixed
        # "http://" to every link that already had it. 原版 bug，已修正：
        # only links without the scheme get it.
        return url if url.lower().startswith(HTTP) else HTTP + url
    return None


def name_tag(name: str, uin: int) -> str:
    """The marker QQTSection puts before a sender's name: "/<name=%d:uin=%u>"
    with the name's GBK byte count (0x1001F3D6, 0x10016C8A, 0x10026EFF)."""
    return f"/<name={len(name.encode('gbk', 'replace'))}:uin={int(uin) & 0xFFFFFFFF}>"


def lobby_line(message) -> str:
    """A lobby chat line as QQTSection 0x1001F3D6 builds it:
    "/<kin=%d>/<level=%d>/<name=%d:uin=%u>%s%s" with the kin flag (when it
    is 0..48, else -1), the sender's points, the name before "说:" and the
    text from "说:" on. A message without a name (0x1002B1FA: "说:" not
    found within its first 20 bytes) shows "说:" as the name."""
    kin = message.kin_flag if 0 <= message.kin_flag <= 0x30 else -1
    if message.text and 0 < len(message.nickname.encode("gbk", "replace")) < 0x14:
        name, text = message.nickname, SAID + message.text
    else:
        name, text = SAID, (message.nickname + SAID + message.text if message.text else message.nickname)
    return f"/<kin={kin}>/<level={message.points}>{name_tag(name, message.uin)}{name}{text}"
