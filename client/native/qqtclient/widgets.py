"""Widget tree built from the scripts' declarative UI classes.

A script describes each widget as a class (``class confirmBtn(TButton)``)
whose attributes come from the class itself or its uiTemplate bases, and whose
nested ``children`` class holds the child widgets. Paths are the dotted names
the scripts pass to the Win_* natives, e.g. ``UI.login.loginDlg.confirmBtn``.
"""

from __future__ import annotations

from typing import Any, Callable, Iterator

from . import scrollbar
from .ui_animation import ImageAnimation

WGTSTYLE_STATIC = 32768
UI_BTN_STYLE_RADIO = 2
EDIT_CODER = 2

TEXT_INPUT_TYPES = {"EDIT", "MULTIEDIT", "RICHEDIT"}

# 1002A16B: E4 bits 8/2/1 select disabled/pressed/hover respectively.
STATE_PRESSED, STATE_HOVER, STATE_NORMAL, STATE_DISABLED = range(4)


def child_classes(cls: type) -> list[tuple[str, type]]:
    holder = getattr(cls, "children", None)
    if not isinstance(holder, type):
        return []
    found: dict[str, type] = {}
    for klass in reversed(holder.__mro__):
        if klass is object:
            continue
        for name, value in vars(klass).items():
            if isinstance(value, type) and not name.startswith("__"):
                found[name] = value
    return list(found.items())


class Widget:
    def __init__(self, path: str, cls: type, parent: "Widget | None", order: int):
        self.path = path
        self.cls = cls
        self.parent = parent
        self.order = order
        self.children: list[Widget] = []

        attr = self.attr
        self.type = str(attr("type", "WIDGET")).upper()
        self.style = int(attr("style", 0) or 0)
        self.extend_style = int(attr("extendstyle", 0) or 0)
        x, y, w, h = (tuple(attr("rect", (0, 0, 0, 0))) + (0, 0, 0, 0))[:4]
        self.x, self.y, self.w, self.h = int(x), int(y), int(w), int(h)
        caption_rect = attr("captionrect", None)
        self.caption_rect = tuple(int(v) for v in caption_rect) if caption_rect else None
        self.hot_rect = attr("hotrect", None)
        self.visible = bool(attr("visible", 1))
        self.enabled = bool(attr("enable", 1))
        self.layer = int(attr("initlayer", 0) or 0)
        self.text = str(attr("caption", "") or "")
        self.image = attr("bkimage", None)
        self.image_offset = tuple(attr("bkimagepos", (0, 0)))
        self.image_flag = int(attr("bkImgFlag", 0) or 0)
        self.framescheme = attr("framescheme", None)
        speed = attr("framespeed")
        loop_speed = attr("loopspeed")
        self.frame_speed = 0.15 if speed is None else float(speed)
        self.loop_speed = 0.0 if loop_speed is None else float(loop_speed)
        self.animation = ImageAnimation()
        # LIFESTRIP / DYLIFESTRIP (gameUI.dll 100117D8, 10011C43).
        self.strip_total = float(attr("totalnum", 0) or 0)
        self.strip_current = float(attr("curnum", 0) or 0)
        self.strip_destination = float(attr("destnum", 0) or 0)
        self.strip_step = float(attr("stepnum", 0) or 0)
        # 10026279 -> 1002650D: default drawcolor is opaque white.
        draw_color = attr("drawcolor")
        self.draw_color = (255, 255, 255, 255) if draw_color is None else tuple(draw_color)
        self.back_color = tuple(attr("bkcolor", (0, 0, 0, 0)))
        self.edge_color = tuple(attr("textEdgeColor", (0, 0, 0, 0)))
        self.text_size = int(attr("textsize", 12) or 12)
        self.text_width = int(attr("textwidth", 0) or 0)
        self.text_height = int(attr("textheight", 0) or 0)
        self.drag_type = int(attr("dragtype", 0) or 0)
        self.text_style = int(attr("textstyle", 0) or 0)
        self.max_char = int(attr("maxchar", 0) or 0)
        self.edit_method = int(attr("editmethod", 0) or 0)
        self.editable = bool(attr("editable", 1))
        self.group_id = int(attr("groupid", 0) or 0)
        self.dark_background = bool(attr("darkBG", 0))
        # DYMASK angles (gameUI.dll 0x10011E82 reads curangle, stepangle and
        # destangle; the constructor 0x10011E2A starts them at 0).
        self.angle_cur = float(attr("curangle", 0) or 0)
        self.angle_step = float(attr("stepangle", 0) or 0)
        self.angle_dest = float(attr("destangle", 0) or 0)
        self.checked = 0
        self.values: dict[int, Any] = {}
        # List lines (value_channel_itemtext) with their colours.
        self.items: list[tuple[str, tuple[int, int, int]]] = []
        # Win_SetDrawTexRect / Win_SetDrawWinRect: draw only this part of the
        # background image, in image coordinates (None draws all of it).
        self.texture_rect: tuple[int, int, int, int] | None = None
        alpha = attr("alphafactor")
        alpha_speed = attr("alphaspeed")
        minimum_alpha = attr("minalphafactor")
        self.alpha = 1.0 if alpha is None else float(alpha)
        self.alpha_speed = 1.0 if alpha_speed is None else float(alpha_speed)
        self.alpha_minimum = 0.0 if minimum_alpha is None else float(minimum_alpha)
        self.alpha_direction = 0
        self.hover = False
        self.pressed = False
        if self.type in ("SCROLLBAR", "SPIN"):
            scrollbar.init(self)
        elif self.type == "TEXTLIST":
            scrollbar.init_list(self)
        if self.type == "KEYEDIT":
            # 0x100139FF: no key yet, and the text starts as "null".
            self.key_value = -1
            self.key_capturing = False
            self.key_blink_on = True
            self.key_blink_ms = 0.0
            self.text = self.text or "null"

    def attr(self, name: str, default: Any = None) -> Any:
        value = getattr(self.cls, name, default)
        return default if callable(value) and not isinstance(value, type) else value

    def handler(self, name: str) -> Callable[..., Any] | None:
        value = getattr(self.cls, name, None)
        return value if callable(value) and not isinstance(value, type) else None

    @property
    def name(self) -> str:
        return self.path.rsplit(".", 1)[-1]

    def absolute(self) -> tuple[int, int]:
        x, y = self.x, self.y
        parent = self.parent
        while parent is not None:
            x += parent.x
            y += parent.y
            parent = parent.parent
        return x, y

    def shown(self) -> bool:
        widget: Widget | None = self
        while widget is not None:
            if not widget.visible:
                return False
            widget = widget.parent
        return True

    def static(self) -> bool:
        return bool(self.style & WGTSTYLE_STATIC)

    def is_radio(self) -> bool:
        return self.type == "CHECK" and bool(self.extend_style & UI_BTN_STYLE_RADIO)

    def password(self) -> bool:
        return self.edit_method == EDIT_CODER

    def hit_rect(self) -> tuple[int, int, int, int]:
        ax, ay = self.absolute()
        if self.hot_rect:
            hx, hy, hw, hh = self.hot_rect
            return ax + hx, ay + hy, hw, hh
        return ax, ay, self.w, self.h

    def state(self) -> int:
        if not self.enabled:
            return STATE_DISABLED
        if self.pressed:
            return STATE_PRESSED
        if self.hover:
            return STATE_HOVER
        return STATE_NORMAL

    def frame_bounds(self, state: int, row_index: int) -> tuple[int, int]:
        """Logical scheme bounds, independent of the image's frame count."""
        scheme = self.framescheme
        if scheme:
            row = scheme[row_index]
            first, last = row[state * 2], row[state * 2 + 1]
        elif self.type == "CHECK":
            first = last = row_index
        elif self.type in ("BUTTON", "HYPERLINK", "SPIN"):
            first = last = state
        else:
            first = last = 0
        return first, last

    def scheme_row(self) -> int:
        if self.type == "CHECK" and self.checked and (not self.framescheme or len(self.framescheme) > 1):
            return 1
        return 0

    def frame_range(self, frame_count: int) -> tuple[int, int] | None:
        """Current logical bounds; image overflow is handled during drawing."""
        first, last = self.frame_bounds(self.state(), self.scheme_row())
        return None if first < 0 else (first, last)

    def advance_animation(self, elapsed_ms: float) -> None:
        self.animation.row = self.scheme_row()
        state = self.state()
        first, last = self.frame_bounds(state, self.animation.row)
        self.animation.advance(elapsed_ms / 1000, state, first, last, self.frame_speed, self.loop_speed)

    def walk(self) -> Iterator["Widget"]:
        yield self
        for child in self.children:
            yield from child.walk()

    def sorted_children(self) -> list["Widget"]:
        return sorted(self.children, key=lambda child: (child.layer, child.order))


def build(path: str, cls: type, parent: Widget | None, order: int = 0) -> Widget:
    widget = Widget(path, cls, parent, order)
    for index, (name, child) in enumerate(child_classes(cls)):
        widget.children.append(build(f"{path}.{name}", child, widget, index))
    return widget
