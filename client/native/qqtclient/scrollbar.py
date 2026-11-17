"""SCROLLBAR and SPIN widgets and the TEXTLIST scroll offset (gameUI.dll).

The scripts' TVScroll is a SCROLLBAR (constructor 0x100088AC, vtable
0x1003D3CC); its TScrollBtn child (a BUTTON with moveable = 1) is the thumb,
and TSpinInc/TSpinDec children are SPIN step buttons (0x10004769). Positions
are integers in [min, max]; the thumb is placed from the position, never the
other way round except while it is dragged. Details and addresses are in
docs/scrollbar-re.md.

Every function that can move a position takes `call(widget, name)`, the
runtime's script dispatcher, because setting a position fires the bar's
OnPosChange (0x10009020) whenever the bar has a thumb.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, Callable

if TYPE_CHECKING:
    from .widgets import Widget

Call = Callable[["Widget", str], Any]

WGTSTYLE_VERTICAL = 8  # +0x184 & 8
WGTSTYLE_PRIMARY = 16  # a SPIN with it steps +1, else -1
WGTSTYLE_SELF_XMOVE = 0x800  # moveable = 1 sets both (0x10028C04)
WGTSTYLE_SELF_YMOVE = 0x1000
EXTEND_RESIZE_BLOCK = 1  # +0x290 & 1: SetRange resizes the thumb
EXTEND_FLIP = 2  # +0x290 & 2: flips the axis (TVScroll's default)
WHEEL_DELTA = 120  # one notch of WM_MOUSEWHEEL
REPEAT_DELAY_S = 0.4  # 0x3FD99999A0000000 in both constructors
CHANNEL_CLIENTRECT = 35


def c_div(a: int, b: int) -> int:
    """idiv: truncate toward zero."""
    quotient = abs(a) // abs(b)
    return quotient if (a < 0) == (b < 0) else -quotient


def init(widget: Widget) -> None:
    """Constructor defaults and the attributes read by 0x10008BEA."""
    widget.scroll_pos = 0  # +0x268
    widget.scroll_min, widget.scroll_max = 0, 10  # +0x26C, +0x270
    widget.page_size = 2  # +0x274
    widget.scroll_extend = 0  # +0x290
    widget.repeat_s = 0.0  # +0x278, time the bar or spin has been held
    if widget.type == "SCROLLBAR":
        widget.scroll_extend = int(widget.attr("extendstyle", 0) or 0)
        span = widget.attr("range")
        if span is not None:
            set_range(widget, int(span[0]), int(span[1]))
        widget.page_size = int(widget.attr("pagesize", 2) or 0)
        # The thumb is not attached yet, so this neither places it nor
        # fires OnPosChange; it only clamps.
        position = widget.attr("pos")
        if position is not None:
            set_pos(widget, int(position), lambda widget, name: None)


def vertical(bar: Widget) -> bool:
    return bool(bar.style & WGTSTYLE_VERTICAL)


def client_rect(widget: Widget) -> tuple[int, int, int, int]:
    """clientrect (+0x220, relative), default the whole widget (0x10027F1F)."""
    rect = widget.values.get(CHANNEL_CLIENTRECT)
    if rect is None:
        return 0, 0, widget.w, widget.h
    return tuple(int(value) for value in rect)  # type: ignore[return-value]


def block(bar: Widget) -> Widget | None:
    """The thumb: AddChild 0x10008933 keeps the last BUTTON child (type 1) as
    relation 9. SPIN children (type 3) get the bar as their relation 6."""
    found = None
    for child in bar.children:
        if child.type == "BUTTON":
            found = child
    return found


def set_range(bar: Widget, low: int, high: int) -> None:
    """0x10008F14: ignored unless low <= high; does not clamp or move the
    position. extendstyle bit 1 sizes the thumb to track - (high - low),
    at least 16 pixels."""
    if low > high:
        return
    bar.scroll_min, bar.scroll_max = low, high
    thumb = block(bar)
    if thumb is not None and bar.scroll_extend & EXTEND_RESIZE_BLOCK:
        _, _, width, height = client_rect(bar)
        if vertical(bar):
            thumb.h = max(height - high + low, 16)
        else:
            thumb.w = max(width - high + low, 16)


def set_pos(bar: Widget, position: int, call: Call) -> int:
    """0x10008FD3 (Win_SetPos, Win_SetScrollPos): clamp to [min, max], place
    the thumb and fire OnPosChange, even when the position did not change."""
    low, high = bar.scroll_min, bar.scroll_max
    if position < low:
        position = low
    elif position > high:
        position = high
    bar.scroll_pos = position
    if place_block(bar):
        call(bar, "OnPosChange")
    return position


def step(bar: Widget, delta: int, call: Call) -> None:
    """0x1000900D."""
    set_pos(bar, bar.scroll_pos + delta, call)


def place_block(bar: Widget) -> bool:
    """0x10009020: move the thumb along the track. Without a thumb nothing
    happens, OnPosChange included; with an inverted range it stays put.

    Vertical bars run bottom-up (position min at the bottom) unless
    extendstyle has bit 2, which TVScroll sets; chat bars clear it.
    Horizontal bars are the other way round. With an empty range the thumb
    goes to the start (vertical, flipped: the bar's own top, not the
    client top) or the end of the track; horizontal, unflipped, it lands one
    thumb length past the start, as the original computes it."""
    thumb = block(bar)
    if thumb is None:
        return False
    cx, cy, cw, ch = client_rect(bar)
    low, high, position = bar.scroll_min, bar.scroll_max, bar.scroll_pos
    flipped = bool(bar.scroll_extend & EXTEND_FLIP)
    if vertical(bar):
        span = ch - thumb.h
        if low < high:
            offset = c_div((position - low) * span, high - low)
            thumb.y = cy + offset if flipped else cy + span - offset
        elif low == high:
            thumb.y = 0 if flipped else cy + span
    else:
        span = cw - thumb.w
        if low < high:
            offset = c_div((position - low) * span, high - low)
            thumb.x = cx + span - offset if flipped else cx + offset
        elif low == high:
            thumb.x = cx + span if flipped else cx + thumb.w
    return True


def notify(bar: Widget, call: Call) -> None:
    """Message 0x14 to relation 6: a TEXTLIST parent (0x10023C76) scrolls to
    scrollspace * position (0x10023F0A). Other parents get nothing."""
    owner = bar.parent
    if owner is not None and owner.type == "TEXTLIST":
        list_scroll_to(owner, owner.scroll_space * bar.scroll_pos, call)


def press_track(bar: Widget, point: tuple[int, int], call: Call) -> None:
    """Event 6 (0x100089B3): a press strictly inside the client area pages
    by pagesize toward the press. Vertical: above the thumb adds pagesize,
    below subtracts it, whatever the axis direction; so bars running
    top-down (map list, server list, replay files) use a negative pagesize."""
    thumb = block(bar)
    if thumb is None or not _inside_client(bar, point):
        return
    tx, ty = thumb.absolute()
    if vertical(bar):
        delta = bar.page_size if point[1] < ty else -bar.page_size
    else:
        delta = bar.page_size if point[0] >= tx else -bar.page_size
    step(bar, delta, call)
    notify(bar, call)


def _inside_client(bar: Widget, point: tuple[int, int]) -> bool:
    """0x10028F26: strictly inside, borders excluded."""
    ax, ay = bar.absolute()
    cx, cy, cw, ch = client_rect(bar)
    return ax + cx < point[0] < ax + cx + cw and ay + cy < point[1] < ay + cy + ch


def tick(widget: Widget, elapsed_s: float, held: bool, mouse: tuple[int, int], call: Call) -> None:
    """Per-frame update of a held bar (0x10008B13) or spin (0x10004811):
    after 0.4 s every frame repeats the step; the timer only resets on
    release, so the repeat does not slow down."""
    if not held:
        widget.repeat_s = 0.0
        return
    widget.repeat_s += elapsed_s
    if widget.repeat_s <= REPEAT_DELAY_S:
        return
    if widget.type == "SPIN":
        call(widget, "OnClick")
        spin_step(widget, call)
        return
    thumb = block(widget)
    if thumb is None:
        return
    tx, ty = thumb.absolute()
    if vertical(widget):
        if mouse[1] < ty:
            step(widget, widget.page_size, call)
        elif mouse[1] > ty + thumb.h:
            step(widget, -widget.page_size, call)
    elif mouse[0] < tx:
        step(widget, -widget.page_size, call)
    elif mouse[0] > tx + thumb.w:
        step(widget, widget.page_size, call)
    notify(widget, call)


def spin_step(spin: Widget, call: Call) -> None:
    """A SPIN click (0x10004879, after OnClick) sends message 0x13 with its
    style to its bar: wgtstyle_primary (TSpinInc) steps +1, otherwise -1,
    only if the result stays in range (0x100089B3)."""
    bar = spin.parent
    if bar is None or bar.type != "SCROLLBAR":
        return
    delta = 1 if spin.style & WGTSTYLE_PRIMARY else -1
    if bar.scroll_min <= bar.scroll_pos + delta <= bar.scroll_max:
        step(bar, delta, call)
    notify(bar, call)


def moveable(widget: Widget) -> int:
    """Style bits from moveable/xmoveable/ymoveable (0x1002B42F -> 0x10028C04)."""
    bits = 0
    if widget.attr("moveable"):
        bits |= WGTSTYLE_SELF_XMOVE | WGTSTYLE_SELF_YMOVE
    if widget.attr("xmoveable"):
        bits |= WGTSTYLE_SELF_XMOVE
    if widget.attr("ymoveable"):
        bits |= WGTSTYLE_SELF_YMOVE
    return bits


def move(widget: Widget, x: int, y: int, call: Call) -> None:
    """Event 0x19 (base 0x1002ADEB): the widget being moved goes to the
    screen point (x, y), clamped by its parent (a bar keeps its thumb in
    the client area, 0x10008E4E), on the axes it may move along; the
    parent then gets 0x1A, which for a bar reads the position back from the
    thumb (0x100091BC)."""
    bar = widget.parent
    if bar is not None and bar.type == "SCROLLBAR":
        ax, ay = bar.absolute()
        cx, cy, cw, ch = client_rect(bar)
        x, y = max(x, ax + cx), max(y, ay + cy)
        if ax + cx + cw < x + widget.w:
            x = ax + cx + cw - widget.w
        if ay + cy + ch < y + widget.h:
            y = ay + cy + ch - widget.h
    bits = moveable(widget)
    px, py = widget.absolute()
    if bits & WGTSTYLE_SELF_XMOVE:
        widget.x += x - px
    if bits & WGTSTYLE_SELF_YMOVE:
        widget.y += y - py
    if bar is not None and bar.type == "SCROLLBAR" and block(bar) is widget:
        block_moved(bar, call)


def block_moved(bar: Widget, call: Call) -> None:
    """0x100091BC: position from the thumb's place on the track, then the
    thumb snaps back to that position."""
    thumb = block(bar)
    if thumb is None:
        return
    cx, cy, cw, ch = client_rect(bar)
    flipped = bool(bar.scroll_extend & EXTEND_FLIP)
    count = bar.scroll_max - bar.scroll_min
    if vertical(bar):
        span, along = ch - thumb.h, thumb.y - cy
        forward = flipped
    else:
        span, along = cw - thumb.w, thumb.x - cx
        forward = not flipped
    delta = 0
    if span >= 1:
        delta = c_div(along * count if forward else (span - along) * count, span)
    set_pos(bar, bar.scroll_min + delta, call)
    notify(bar, call)


# TEXTLIST ---------------------------------------------------------------


def init_list(widget: Widget) -> None:
    """TEXTLIST (0x10023B63): scrollspace defaults to 1 (+0x2A8). The offset
    (+0x2A0) counts pixels up from the newest line."""
    widget.scroll_space = int(widget.attr("scrollspace", 1) or 1)
    widget.list_offset = 0
    widget.list_total = 0
    widget.list_layout_key = None


def list_client_rect(widget: Widget) -> tuple[int, int, int, int]:
    """The list's text area: clientrect, which the scripts set for every
    chat list. Without one the host keeps its older captionrect fallback
    (the original uses the whole widget, 0x10027F1F)."""
    x, y, w, h = widget.values.get(CHANNEL_CLIENTRECT) or widget.caption_rect or (0, 0, widget.w, widget.h)
    return int(x), int(y), int(w) or widget.w, int(h) or widget.h


def list_bar(widget: Widget) -> Widget | None:
    """Relation 7: the TEXTLIST's SCROLLBAR child (0x10023C76)."""
    found = None
    for child in widget.children:
        if child.type == "SCROLLBAR":
            found = child
    return found


def list_scroll_to(widget: Widget, offset: int, call: Call) -> None:
    """0x100253C8: clamp the offset to [0, total - height], then set the bar
    to ceil(offset / scrollspace)."""
    _, _, _, height = list_client_rect(widget)
    offset = min(offset, widget.list_total - height)
    widget.list_offset = max(offset, 0)
    bar = list_bar(widget)
    if bar is not None:
        space = widget.scroll_space
        set_pos(bar, c_div(widget.list_offset - 1 + space, space), call)


def list_layout(widget: Widget, total: int, call: Call) -> int:
    """0x1002559C, run when the list's content or size changed: the bar is
    enabled only while the text is taller than the client area, its range is
    ceil((total - height) / scrollspace) and the offset is clamped again.
    Returns the offset to draw with."""
    widget.list_total = total
    bar = list_bar(widget)
    key = (total, list_client_rect(widget), widget.scroll_space,
           None if bar is None else (client_rect(bar), bar.w, bar.h))
    if key == widget.list_layout_key:
        return widget.list_offset
    widget.list_layout_key = key
    _, _, _, height = list_client_rect(widget)
    if bar is not None:
        bar.enabled = height < total
        space = widget.scroll_space
        set_range(bar, 0, max(c_div(total - height - 1 + space, space), 0))
    list_scroll_to(widget, widget.list_offset, call)
    return widget.list_offset


def wheel(target: Widget | None, notches: int, call: Call) -> None:
    """WM_MOUSEWHEEL (dispatcher 0x100304C8) goes to the focused widget, else
    the capture, else the hovered one, as message 0x11. Only TEXTLIST
    (0x10023F0A) and LISTBOX handle it; everything else, scroll bars
    included, passes it to its parent. A TEXTLIST adds the wheel delta
    (120 a notch, positive away from the user) to its pixel offset."""
    widget = target
    while widget is not None:
        if widget.type == "TEXTLIST":
            list_scroll_to(widget, widget.list_offset + notches * WHEEL_DELTA, call)
            return
        widget = widget.parent
