"""Custom keyboard layout: the setup dialog's KEYEDIT boxes, the stored
26-byte layout and the battle keys built from it.

Client.exe keeps one layout per QQ number under the key "keyboard_layout2"
(26 Windows virtual-key bytes; defaults 0x0048AE40). sc_keyLayout_restore
(0x0048DC20) copies it into the script's ``keyLayout`` class, and
sc_keyLayout_save (0x0048E000) reads the class back. The battle reloads it
when a scene starts (0x005A2AB7, practice 0x00458CC0) and converts each
virtual key to a DirectInput scan code through the table at 0x007F2424; keys
without an entry (Alt, the punctuation keys) do nothing in battle.

The host stores the layout in the client directory's profile/<uin>.keys, next
to the prop bar's profile/<uin>.ini.
"""

from __future__ import annotations

import pathlib
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Callable

import pygame

from .battle.sidecar import MOVE_DOWN, MOVE_LEFT, MOVE_RIGHT, MOVE_UP

if TYPE_CHECKING:
    from .widgets import Widget

LAYOUT_SIZE = 26
# 0x0048AE40: left, right, up, down, bubble, switch item, smart use, chat,
# custom chat, items 1-7, shop 1-7, setting, quick help, and an unused 'Q'.
DEFAULT_LAYOUT = bytes((0x25, 0x27, 0x26, 0x28, 0x20, 0x09, 0x11, 0x0D, 0x0D,
                        0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37,
                        0x38, 0x39, 0x30, 0x6D, 0x49, 0x4F, 0x50,
                        0x7B, 0x70, 0x51))
# Script attribute for each byte, in the order 0x0048DC20 writes them. Byte 25
# has no attribute; sc_keyLayout_save keeps the default there.
FIELDS = ("moveLeft", "moveRight", "moveUp", "moveDown", "putBomb", "switchItem", "smartUseItem", "chat",
          "customChat", *(f"useItem{i}" for i in range(7)), *(f"shop{i}" for i in range(7)), "setting", "quickHelp")

# gameUI.dll 0x10013BC3: names shown by KEYEDIT. Letters and digits show the
# character itself (0x10014754); any other key is not accepted.
KEY_NAMES = {
    0x08: "Back", 0x09: "Tab", 0x0D: "Enter", 0x10: "Shift", 0x11: "Ctrl", 0x12: "Alt", 0x13: "Pause",
    0x20: "Space", 0x21: "PageUp", 0x22: "PageDn", 0x23: "End", 0x24: "Home", 0x25: "Left", 0x26: "Up",
    0x27: "Right", 0x28: "Down", 0x2D: "Insert", 0x2E: "Delete",
    **{0x60 + i: f"Numpad{i}" for i in range(10)},
    0x6A: "Numpad*", 0x6B: "Numpad+", 0x6D: "Numpad-", 0x6E: "Numpad.", 0x6F: "Numpad/",
    **{0x70 + i: f"F{i + 1}" for i in range(12)},
    0xBA: ";", 0xBB: "=", 0xBC: ",", 0xBD: "-", 0xBE: ".", 0xBF: "/", 0xC0: "`",
    0xDB: "[", 0xDC: "\\", 0xDD: "]", 0xDE: "'",
}
VK_RETURN, VK_ESCAPE, VK_CONTROL = 0x0D, 0x1B, 0x11
VK_LCONTROL, VK_RCONTROL = 0xA2, 0xA3

# pygame key -> Windows virtual key. Cmd counts as Ctrl, as for accelerators.
PYGAME_VK = {
    pygame.K_BACKSPACE: 0x08, pygame.K_TAB: 0x09, pygame.K_RETURN: 0x0D, pygame.K_KP_ENTER: 0x0D,
    pygame.K_LSHIFT: 0x10, pygame.K_RSHIFT: 0x10, pygame.K_LCTRL: 0x11, pygame.K_RCTRL: 0x11,
    pygame.K_LMETA: 0x11, pygame.K_RMETA: 0x11, pygame.K_LALT: 0x12, pygame.K_RALT: 0x12,
    pygame.K_PAUSE: 0x13, pygame.K_ESCAPE: 0x1B, pygame.K_SPACE: 0x20, pygame.K_PAGEUP: 0x21,
    pygame.K_PAGEDOWN: 0x22, pygame.K_END: 0x23, pygame.K_HOME: 0x24, pygame.K_LEFT: 0x25,
    pygame.K_UP: 0x26, pygame.K_RIGHT: 0x27, pygame.K_DOWN: 0x28, pygame.K_INSERT: 0x2D,
    pygame.K_DELETE: 0x2E, pygame.K_KP_MULTIPLY: 0x6A, pygame.K_KP_PLUS: 0x6B, pygame.K_KP_MINUS: 0x6D,
    pygame.K_KP_PERIOD: 0x6E, pygame.K_KP_DIVIDE: 0x6F, pygame.K_SEMICOLON: 0xBA, pygame.K_EQUALS: 0xBB,
    pygame.K_COMMA: 0xBC, pygame.K_MINUS: 0xBD, pygame.K_PERIOD: 0xBE, pygame.K_SLASH: 0xBF,
    pygame.K_BACKQUOTE: 0xC0, pygame.K_LEFTBRACKET: 0xDB, pygame.K_BACKSLASH: 0xDC,
    pygame.K_RIGHTBRACKET: 0xDD, pygame.K_QUOTE: 0xDE,
    **{pygame.K_a + i: 0x41 + i for i in range(26)},
    **{pygame.K_0 + i: 0x30 + i for i in range(10)},
    **{pygame.K_KP0 + i: 0x60 + i for i in range(10)},
    **{pygame.K_F1 + i: 0x70 + i for i in range(12)},
}
# 0x007F2424 maps a virtual key to one DirectInput scan code; the physical
# key it names, as pygame keys. VK_RETURN becomes DIK_NUMPADENTER (0x9C) and
# VK_SHIFT the left Shift only.
BATTLE_VK_KEYS = {
    0x08: (pygame.K_BACKSPACE,), 0x09: (pygame.K_TAB,), 0x0C: (pygame.K_KP5,), 0x0D: (pygame.K_KP_ENTER,),
    0x10: (pygame.K_LSHIFT,), VK_LCONTROL: (pygame.K_LCTRL,), VK_RCONTROL: (pygame.K_RCTRL,),
    0xA4: (pygame.K_LALT,), 0x14: (pygame.K_CAPSLOCK,), 0x1B: (pygame.K_ESCAPE,), 0x20: (pygame.K_SPACE,),
    0x21: (pygame.K_PAGEUP,), 0x22: (pygame.K_PAGEDOWN,), 0x23: (pygame.K_END,), 0x24: (pygame.K_HOME,),
    0x25: (pygame.K_LEFT,), 0x26: (pygame.K_UP,), 0x27: (pygame.K_RIGHT,), 0x28: (pygame.K_DOWN,),
    0x2D: (pygame.K_INSERT,), 0x2E: (pygame.K_DELETE,), 0x6A: (pygame.K_KP_MULTIPLY,),
    0x6B: (pygame.K_KP_PLUS,), 0x6D: (pygame.K_KP_MINUS,), 0x6E: (pygame.K_KP_PERIOD,),
    0x6F: (pygame.K_KP_DIVIDE,), 0x90: (pygame.K_NUMLOCK,), 0x91: (pygame.K_SCROLLLOCK,),
    **{0x30 + i: (pygame.K_0 + i,) for i in range(10)},
    **{0x41 + i: (pygame.K_a + i,) for i in range(26)},
    **{0x60 + i: (pygame.K_KP0 + i,) for i in range(10)},
    **{0x70 + i: (pygame.K_F1 + i,) for i in range(12)},
}
# Keys that 0x005A2AB7 never overwrites (0x005A4B6E): Z..M also use item
# slots 0..6.
FIXED_SLOT_KEYS = (pygame.K_z, pygame.K_x, pygame.K_c, pygame.K_v, pygame.K_b, pygame.K_n, pygame.K_m)


def key_name(vk: int) -> str:
    """0x10014754 with no modifiers."""
    if 0x41 <= vk <= 0x5A or 0x30 <= vk <= 0x39:
        return chr(vk)
    return KEY_NAMES.get(vk, "")


def profile_path(uin: int) -> pathlib.Path:
    return pathlib.Path("profile") / f"{int(uin)}.keys"


def load(uin: int) -> bytes:
    try:
        data = bytes.fromhex(profile_path(uin).read_text().strip())
    except (OSError, ValueError):
        return DEFAULT_LAYOUT
    return data if len(data) == LAYOUT_SIZE else DEFAULT_LAYOUT


def store(uin: int, layout: bytes) -> None:
    path = profile_path(uin)
    path.parent.mkdir(exist_ok=True)
    path.write_text(layout.hex() + "\n")


# KEYEDIT (gameUI.dll vtable 0x1003E2B8) --------------------------------------
#
# A box shows its key's name. A click (0x10014AC9, event 7) or Enter starts
# capturing; while capturing it blinks "-" (700 ms shown, 400 ms hidden,
# 0x100146E4) and the next accepted key (0x1001491F) becomes its value.
# Clicking again shows "Mouse L" (right click "Mouse R") without changing the
# value, Escape or losing focus stops capturing, and unnamed keys are
# ignored. While the box has focus every key stays in it.

BLINK_SHOWN_MS, BLINK_HIDDEN_MS = 700, 400


def set_key_value(widget: Widget, value: Any) -> None:
    widget.key_value = int(value)
    widget.text = key_name(widget.key_value)


def keyedit_click(widget: Widget, right: bool = False) -> None:
    if not widget.key_capturing:
        widget.key_capturing = True
        widget.key_blink_on, widget.key_blink_ms = True, 0
    else:
        widget.text = "Mouse R" if right else "Mouse L"
        widget.key_capturing = False


def keyedit_key(widget: Widget, key: int) -> None:
    vk = PYGAME_VK.get(key)
    if vk == VK_RETURN and not widget.key_capturing:
        keyedit_click(widget)
    elif vk == VK_ESCAPE:
        widget.key_capturing = False
    elif widget.key_capturing and vk is not None and key_name(vk):
        set_key_value(widget, vk)
        widget.key_capturing = False


def keyedit_tick(widget: Widget, elapsed_ms: float) -> None:
    if not widget.key_capturing:
        return
    widget.key_blink_ms += elapsed_ms
    if widget.key_blink_ms > (BLINK_SHOWN_MS if widget.key_blink_on else BLINK_HIDDEN_MS):
        widget.key_blink_ms = 0
        widget.key_blink_on = not widget.key_blink_on


def keyedit_text(widget: Widget) -> str:
    """0x10014630: a capturing box draws only the blinking dash."""
    if widget.key_capturing:
        return "-" if widget.key_blink_on else ""
    return widget.text


# Natives ----------------------------------------------------------------------


def build_keylayout_natives(namespace: dict[str, Any]) -> dict[str, Callable[..., Any]]:
    def script_layout() -> Any:
        return namespace.get("keyLayout")

    def sc_keyLayout_restore(uin):
        target = script_layout()
        if target is None:
            return
        layout = load(uin)
        for name, value in zip(FIELDS, layout):
            setattr(target, name, value)
        # 0x0048DC20 writes keyLayout.setting, but the dialog shows and edits
        # keyLayout.system, so the original never displays or keeps it.
        # Original bug, fixed: both names carry the setting key.
        target.system = layout[FIELDS.index("setting")]

    def sc_keyLayout_save(uin):
        target = script_layout()
        if target is None:
            return
        layout = bytearray(DEFAULT_LAYOUT)
        for index, name in enumerate(FIELDS):
            value = getattr(target, "system" if name == "setting" else name, -1)
            if name == "quickHelp" and int(value) < 0:
                continue  # the dialog has no quick-help box
            layout[index] = int(value) & 0xFF
        try:
            store(uin, bytes(layout))
        except OSError as error:
            print(f"[keylayout] cannot save the keyboard layout: {error}", flush=True)

    return {"sc_keyLayout_restore": sc_keyLayout_restore, "sc_keyLayout_save": sc_keyLayout_save}


# Battle keys --------------------------------------------------------------------


@dataclass
class BattleKeys:
    """The scan-code slots 0x005A2AB7 fills, as pygame keys."""

    moves: dict[int, int] = field(default_factory=dict)
    bomb: frozenset[int] = frozenset()
    smart_use: frozenset[int] = frozenset()
    slots: dict[int, int] = field(default_factory=dict)
    page: frozenset[int] = frozenset()

    @classmethod
    def from_layout(cls, layout: bytes) -> "BattleKeys":
        def keys(vk: int) -> tuple[int, ...]:
            return BATTLE_VK_KEYS.get(vk, ())

        moves: dict[int, int] = {}
        for vk, move in ((layout[0], MOVE_LEFT), (layout[1], MOVE_RIGHT), (layout[2], MOVE_UP), (layout[3], MOVE_DOWN)):
            for key in keys(vk):
                moves.setdefault(key, move)
        smart = layout[6]
        smart_keys = keys(VK_LCONTROL) + keys(VK_RCONTROL) if smart == VK_CONTROL else keys(smart)
        slots = {key: index for index, key in enumerate(FIXED_SLOT_KEYS)}
        for index in range(7):
            for key in keys(layout[9 + index]):
                slots[key] = index
        return cls(moves, frozenset(keys(layout[4])), frozenset(smart_keys), slots, frozenset(keys(layout[5])))

    @classmethod
    def for_uin(cls, uin: int | None) -> "BattleKeys":
        return cls.from_layout(load(uin) if uin else DEFAULT_LAYOUT)
