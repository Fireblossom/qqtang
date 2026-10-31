"""Widget tree built from the scripts' declarative UI classes.

A script describes each widget as a class (``class confirmBtn(TButton)``)
whose attributes come from the class itself or its uiTemplate bases, and whose
nested ``children`` class holds the child widgets. Paths are the dotted names
the scripts pass to the Win_* natives, e.g. ``UI.login.loginDlg.confirmBtn``.
"""

from __future__ import annotations

from typing import Any, Callable, Iterator

WGTSTYLE_STATIC = 32768
UI_BTN_STYLE_RADIO = 2
EDIT_CODER = 2

TEXT_INPUT_TYPES = {"EDIT", "MULTIEDIT", "RICHEDIT", "KEYEDIT"}

# framescheme rows hold four (first, last) frame pairs. Comparing the schemes
# with the frames of their images shows the state order below: for example
# btn_cross.img is (dim, normal, highlight, normal) with (0,0,2,2,3,3,1,1),
# and hover-only overlays use (0,0,0,0,-1,-1,-1,-1).
STATE_DISABLED, STATE_HOVER, STATE_PRESSED, STATE_NORMAL = range(4)
# Buttons without a scheme store their frames as normal, hover, pressed, disabled.
DEFAULT_BUTTON_FRAMES = {STATE_NORMAL: 0, STATE_HOVER: 1, STATE_PRESSED: 2, STATE_DISABLED: 3}


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
        self.frame_speed = float(attr("framespeed", 0) or 0)
        # LIFESTRIP / DYLIFESTRIP (gameUI.dll 100117D8, 10011C43).
        self.strip_total = float(attr("totalnum", 0) or 0)
        self.strip_current = float(attr("curnum", 0) or 0)
        self.strip_destination = float(attr("destnum", 0) or 0)
        self.strip_step = float(attr("stepnum", 0) or 0)
        self.draw_color = tuple(attr("drawcolor", (0, 0, 0, 255)))
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
        self.checked = 0
        self.values: dict[int, Any] = {}
        # List lines (value_channel_itemtext) with their colours.
        self.items: list[tuple[str, tuple[int, int, int]]] = []
        # Win_SetDrawTexRect / Win_SetDrawWinRect: draw only this part of the
        # background image, in image coordinates (None draws all of it).
        self.texture_rect: tuple[int, int, int, int] | None = None
        self.alpha = 1.0
        self.alpha_speed = 0.0
        self.alpha_direction = 0
        self.hover = False
        self.pressed = False

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
        if self.pressed and self.hover:
            return STATE_PRESSED
        if self.hover:
            return STATE_HOVER
        return STATE_NORMAL

    def frame_range(self, frame_count: int) -> tuple[int, int] | None:
        """Frames to show for the current state, or None to draw nothing."""
        state = self.state()
        scheme = self.framescheme
        if scheme:
            row = scheme[1] if self.checked and len(scheme) > 1 else scheme[0]
            first, last = row[state * 2], row[state * 2 + 1]
            if first < 0:
                return None
        elif self.type in ("BUTTON", "CHECK"):
            first = last = DEFAULT_BUTTON_FRAMES[state]
        elif frame_count > 1:
            first, last = 0, frame_count - 1
        else:
            first = last = 0
        if first >= frame_count:
            first = last = 0
        return first, min(last, frame_count - 1)

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
