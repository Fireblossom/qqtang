"""The section's 500 ms timer (QQTSection XSection::OnTimer 0x1002120A) and
the small-bugle queue it drives (CBugleHandler), docs/section-timer-re.md.

A successful section login (responselogin 0x1001B2EA) starts a 500 ms
Windows timer (0x10021094, SetTimer in 0x1003DAF7). Every tick runs the
bugle queue (0x10008FD8); every second tick, the handler at XSection+0x30
(0x10016FC2) runs the scripts' updatePlayer() and updatePlayer2(), so both
lobby player lists are redrawn once a second.
"""

from __future__ import annotations

import collections
import dataclasses
from typing import Any, Callable

TICK_MS = 500  # 0x10021094: thunk_FUN_1003daf7(timer, 500, 0)
QUEUE_LIMIT = 50  # 0x10008A0F drops the oldest while more than 49 wait
FAST_BACKLOG = 10  # 0x10008FD8: from 10 waiting, one is shown every tick
REMOVE_TICKS = (0x38, 0x3A, 0x3C)  # the three display slots' countdowns (+0x54/+0x56/+0x58)
POPO_TICKS = 6  # +0x2C counts to 6 while the queue is empty
NAME_LIMIT = 0x14  # 0x1002B1FA: the name is the 1..19 bytes before "说:"
SAID = "说:"
OFFICIAL_IDENTITY = 0x70006  # 0x10008A0F: these viewers see "[uin]" and the whole line


@dataclasses.dataclass
class Bugle:
    """The 0x11C-byte queue record: u32 UIN, char name[20], char text[256],
    u32 effect (NOTIFY_SECTION_MSG +0x40E)."""
    uin: int
    name: str
    text: str
    effect: int


def split_bugle(line: str, uin: int, viewer_identity: int = 0) -> tuple[str, str]:
    """The (name, text) 0x10008A0F queues for a received "<name>说:<text>":
    the line is cleaned by 0x100086BA (trailing CR/LF trimmed, inner LF to a
    space), then split by 0x1002B1FA in front of "说:", which stays with the
    text. With no marker in byte 1..19 the name is "说:" itself and the text
    the whole line. A viewer with an official identity bit (+0x1C8 &
    0x70006, the login reply's identity, inferred) gets "[uin]" instead."""
    line = line.rstrip("\r\n").replace("\n", " ")
    if viewer_identity & OFFICIAL_IDENTITY:
        return f"[{uin & 0xFFFFFFFF}]", line
    # 0x100086BA escapes \ " ' (0x10008D10, CString::Find) for the script
    # call before the split, so the byte tests count them twice; the script
    # parser takes the backslashes out again.
    escaped = line.replace("\\", "\\\\").replace('"', '\\"').replace("'", "\\'").encode("gbk", "replace")
    at = escaped.find(SAID.encode("gbk"))
    if 0 < at < NAME_LIMIT and at + 2 < len(escaped):
        name, _, text = line.partition(SAID)
        return name, SAID + text
    return SAID, line


class BugleQueue:
    """CBugleHandler's queue (CList at +0x38, count +0x44) and display
    timing. Script callbacks go through call(name, *args)."""

    def __init__(self, call: Callable[..., Any]):
        self.call = call
        self.entries: collections.deque[Bugle] = collections.deque()
        self.alternate = 0  # +0x28
        self.popo = 0  # +0x2C
        self.countdowns = [-1, -1, -1]  # +0x54..+0x58 (shorts)

    def push(self, bugle: Bugle) -> None:
        """0x10008A0F (from the 0x0092 handler 0x1001F3D6 for identity bit 14)."""
        while len(self.entries) >= QUEUE_LIMIT:
            self.entries.popleft()
        self.entries.append(bugle)

    def show(self) -> None:
        """0x10008D7E: the head goes to NotifyBroadcast between
        NotifyRemovePopo() and NotifyAddPopo(effect); then it is dropped."""
        bugle = self.entries.popleft()
        self.call("NotifyRemovePopo")
        self.call("NotifyBroadcast", bugle.name, bugle.uin, bugle.text)
        self.call("NotifyAddPopo", bugle.effect)

    def tick(self) -> None:
        """0x10008FD8, every 500 ms."""
        if self.entries:
            self.countdowns = [-1, -1, -1]
        if len(self.entries) >= FAST_BACKLOG:
            self.show()
            if len(self.entries) < FAST_BACKLOG:
                self.alternate = 0
        if 0 < len(self.entries) < FAST_BACKLOG:
            self.alternate = (self.alternate + 1) % 2
            if self.alternate == 0:
                self.show()
                if not self.entries:
                    self.popo = 0
                    self.countdowns = list(REMOVE_TICKS)
        if not self.entries:
            self.popo = (self.popo + 1) % POPO_TICKS
            if self.popo == 0:
                self.call("NotifyRemovePopo")
            for slot in range(len(self.countdowns)):
                if self.countdowns[slot] >= 0:
                    self.countdowns[slot] -= 1
                if self.countdowns[slot] == 0:
                    self.call("NotifyRemoveMsg")


class SectionTimer:
    def __init__(self, call: Callable[..., Any], connected: Callable[[], bool]):
        self.call = call
        self.connected = connected
        self.bugles = BugleQueue(call)
        self.running = False
        self.elapsed = 0
        self.second = 0  # XSection +0xC548 (param_1[0x3152])

    def start(self) -> None:
        """0x10021094 (re)creates the timer: the phase starts over."""
        self.running, self.elapsed = True, 0

    def advance(self, elapsed_ms: int) -> None:
        if not self.running:
            return
        self.elapsed += int(elapsed_ms)
        if self.elapsed < TICK_MS:
            return
        # Windows keeps at most one WM_TIMER pending: a long frame gives one tick.
        self.elapsed = (self.elapsed - TICK_MS) % TICK_MS
        self.tick()

    def tick(self) -> None:
        """0x1002120A (the parts the host has: bugles and the list redraw)."""
        self.bugles.tick()
        self.second = (self.second + 1) % 2
        if self.second != 0:
            return
        # 0x10016FC2. The original timer keeps running after a logout; the
        # lists' natives have nothing to show then, so the host skips it.
        if self.connected():
            self.call("updatePlayer")
            self.call("updatePlayer2")
