"""Drawing of the widget tree with pygame."""

from __future__ import annotations

import pathlib
import time
import math
from typing import Callable

import pygame

from . import dimg
from .resources import Resources
from .script import log
from .widgets import TEXT_INPUT_TYPES, Widget
from .ui_alpha import alpha_byte, combined_alpha, float32

DT_CENTER, DT_RIGHT, DT_VCENTER, DT_BOTTOM = 1, 2, 4, 8
FONT_CANDIDATES = (
    "/System/Library/Fonts/Hiragino Sans GB.ttc",
    "/System/Library/Fonts/STHeiti Light.ttc",
)
DEFAULT_FRAME_MS = 100
# Glyph order of the number*.img strips (verified against number2.img) and of
# the per-frame PICLABEL fonts (num_player.img: digits, then '/').
NUMBER_GLYPHS = "0123456789/:-+."
TEXT_CACHE_LIMIT = 4096
WRAPPED_TYPES = ("LABEL", "DYLABEL", "MESSAGEBOX")


def remaining_angle_image(source: pygame.Surface, angle: float) -> pygame.Surface:
    """DYMASK: erase clockwise from twelve o'clock (gameUI 0x100120E7).

    The native triangle fans reach the rectangle's corners at 45/135/225/315
    degrees, so calculate the ray in normalized image coordinates.
    """
    picture = source.copy()
    if not 0 <= angle < 360:
        picture.fill((0, 0, 0, 0))
        return picture
    if angle == 0:
        return picture
    width, height = picture.get_size()
    cx, cy = width / 2, height / 2
    radians = math.radians(angle)
    dx, dy = math.sin(radians), -math.cos(radians)
    distance = 1 / max(abs(dx), abs(dy))
    points = [(cx, cy), (cx + cx * dx * distance, cy + cy * dy * distance)]
    points += [point for corner, point in ((45, (width, 0)), (135, (width, height)),
                                          (225, (0, height)), (315, (0, 0))) if corner > angle]
    points.append((cx, 0))
    mask = pygame.Surface((width, height), pygame.SRCALPHA)
    mask.fill((255, 255, 255, 0))
    pygame.draw.polygon(mask, (255, 255, 255, 255), points)
    picture.blit(mask, (0, 0), special_flags=pygame.BLEND_RGBA_MULT)
    return picture


def cell_width(text: str, size: int) -> int:
    """Width of text in the original's 宋体 bitmap font, which the scripts lay
    out against (e.g. len(name) * 6 at size 12): a GBK single-byte character
    takes half the size, a double-byte one twice that."""
    return len(text.encode("gbk", "replace")) * (size // 2)


class SpriteCache:
    def __init__(self, resources: Resources):
        self.resources = resources
        self.sprites: dict[str, list[tuple[pygame.Surface | None, int, int]] | None] = {}
        self.canvases: dict[str, tuple[int, int]] = {}
        self.directions: dict[str, int] = {}
        self.aliases: dict[str, str] = {}

    def get(self, path: str) -> list[tuple[pygame.Surface | None, int, int]] | None:
        key = path.lower()
        if key not in self.sprites:
            self.sprites[key] = self._load(path, key)
        return self.sprites[key]

    def canvas(self, path: str) -> tuple[int, int]:
        """The DIMG canvas size; frames sit on it at their own offsets."""
        self.get(path)
        return self.canvases.get(path.lower(), (0, 0))

    def _load(self, path: str, key: str) -> list[tuple[pygame.Surface | None, int, int]] | None:
        data = self.resources.read(path)
        if data is None and key in self.aliases:
            data = self.resources.read(self.aliases[key])
        if data is None:
            log(f"[missing image] {path}")
            return None
        try:
            sprite = dimg.parse(data)
        except dimg.DIMGError as error:
            log(f"[bad image] {path}: {error}")
            return None
        self.canvases[key] = (sprite.width, sprite.height)
        self.directions[key] = sprite.directions
        frames = []
        for frame in sprite.frames:
            if frame.pixels is None:
                frames.append((None, frame.x, frame.y))
                continue
            height, width = frame.pixels.shape[:2]
            surface = pygame.image.frombuffer(frame.pixels.tobytes(), (width, height), "RGBA").convert_alpha()
            frames.append((surface, frame.x, frame.y))
        return frames


class Renderer:
    def __init__(self, screen: pygame.Surface, resources: Resources):
        self.screen = screen
        self.sprites = SpriteCache(resources)
        font_path = next((path for path in FONT_CANDIDATES if pathlib.Path(path).exists()), None)
        self.font_path = font_path
        self.fonts: dict[int, pygame.font.Font] = {}
        self.text_cache: dict[tuple[str, int, tuple[int, int, int]], pygame.Surface] = {}
        self.started = time.monotonic()
        # Host drawing for widgets with `callbackdraw` (e.g. SC_room_drawPlayer).
        self.draw_callbacks: dict[str, Callable[[pygame.Surface], None]] = {}
        # Host drawing below and above the widget tree (the battle field and
        # its player rows around UI.game).
        self.underlay: Callable[[pygame.Surface], None] | None = None
        self.overlay: Callable[[pygame.Surface], None] | None = None

    def font(self, size: int) -> pygame.font.Font:
        if size not in self.fonts:
            self.fonts[size] = pygame.font.Font(self.font_path, size)
        return self.fonts[size]

    def text_line(self, line: str, size: int, color: tuple[int, int, int]) -> pygame.Surface:
        """One line of text, squeezed to its 宋体 width where this font is wider."""
        key = (line, size, color)
        surface = self.text_cache.get(key)
        if surface is None:
            surface = self.font(size).render(line, True, color)
            width = cell_width(line, size)
            if 0 < width < surface.get_width():
                surface = pygame.transform.smoothscale(surface, (width, surface.get_height()))
            if len(self.text_cache) >= TEXT_CACHE_LIMIT:
                self.text_cache.clear()
            self.text_cache[key] = surface
        return surface

    def draw(self, root: Widget, focus: Widget | None, drag: tuple[str, tuple[int, int]] | None = None) -> None:
        """drag: the dragged item's image and the mouse position."""
        self.screen.fill((0, 0, 0))
        if self.underlay is not None:
            self.underlay(self.screen)
        self._draw(root, 0, 0, 1.0, focus)
        if self.overlay is not None:
            self.overlay(self.screen)
        if drag is not None:
            frames = self.sprites.get(drag[0])
            if frames and frames[0][0] is not None:
                image = frames[0][0]
                self.screen.blit(image, (drag[1][0] - image.get_width() // 2, drag[1][1] - image.get_height() // 2))

    def _draw(self, widget: Widget, parent_x: int, parent_y: int, parent_alpha: float, focus: Widget | None) -> None:
        if not widget.visible:
            return
        x, y = parent_x + widget.x, parent_y + widget.y
        alpha = combined_alpha(widget.alpha, parent_alpha if widget.parent is not None else None)
        if alpha <= 0:
            return
        if widget.dark_background:
            shade = pygame.Surface(self.screen.get_size(), pygame.SRCALPHA)
            shade.fill((0, 0, 0, 96))
            self.screen.blit(shade, (0, 0))
        if widget.back_color[3] > 0 and widget.w > 0 and widget.h > 0:
            fill = pygame.Surface((widget.w, widget.h), pygame.SRCALPHA)
            r, g, b, a = widget.back_color
            fill.fill((r, g, b, alpha_byte(alpha, a)))
            self.screen.blit(fill, (x, y))
        if widget.type == "NUMLABEL":
            self._draw_number(widget, x, y, alpha)
        elif widget.type in ("DYSTRIP", "LIFESTRIP", "DYLIFESTRIP"):
            self._draw_life_strip(widget, x, y, alpha)
        elif widget.type == "PICLABEL":
            self._draw_glyph_frames(widget, x, y, alpha)
        elif widget.type == "DYLABEL":
            # A tip box without text draws nothing, not even its image (0x1001132C).
            if widget.text.strip("\n"):
                self._draw_tip_box(widget, x, y, alpha)
                self._draw_text(widget, x, y, alpha, False)
        elif widget.type == "DYMASK":
            if 0 <= widget.angle_cur < 360 and widget.image:
                self._draw_image(widget, x, y, alpha, angle=widget.angle_cur)
        else:
            if widget.image:
                self._draw_image(widget, x, y, alpha)
            if widget.text or (widget.type in TEXT_INPUT_TYPES and widget is focus):
                self._draw_text(widget, x, y, alpha, widget is focus)
        if widget.type == "TABWIN":
            self._draw_hotcover(widget, x, y, alpha)
        if widget.items:
            self._draw_items(widget, x, y, alpha)
        callback = self.draw_callbacks.get(widget.attr("callbackdraw", ""))
        if callback is not None:
            callback(self.screen)
        # A tab draws its children only while selected (0x10004D27); they
        # stay visible and hit-testable.
        if widget.type == "TABWIN" and not widget.checked:
            return
        for child in widget.sorted_children():
            self._draw(child, x, y, alpha, focus)

    def draw_widget(self, widget: Widget | None) -> None:
        """Paint a UI subtree above the host's result-table overlay."""
        if widget is None or not widget.shown():
            return
        parent = widget.parent
        x, y = parent.absolute() if parent is not None else (0, 0)
        ancestors = []
        while parent is not None:
            ancestors.append(parent)
            parent = parent.parent
        alpha = None
        for ancestor in reversed(ancestors):
            alpha = combined_alpha(ancestor.alpha, alpha)
        self._draw(widget, x, y, 1.0 if alpha is None else alpha, None)

    def _draw_life_strip(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """1001192F: keep both end thirds and stretch the middle third."""
        current, total = float32(widget.strip_current), float32(widget.strip_total)
        if not float32(0.01) < current <= total or not widget.image:
            return
        frames = self.sprites.get(widget.image)
        if not frames or frames[0][0] is None:
            return
        source, ox, oy = frames[0]
        width = int(float32(float32(widget.w * current) / total))
        if width < 1:
            return
        cap = source.get_width() // 3
        if cap < 1 or width < 2 * cap + 1:
            image = pygame.transform.scale(source, (width, source.get_height()))
        else:
            image = pygame.Surface((width, source.get_height()), pygame.SRCALPHA)
            image.blit(source, (0, 0), (0, 0, cap, source.get_height()))
            middle = source.subsurface((cap, 0, cap, source.get_height()))
            image.blit(pygame.transform.scale(middle, (width - 2 * cap, source.get_height())), (cap, 0))
            image.blit(source, (width - cap, 0), (source.get_width() - cap, 0, cap, source.get_height()))
        image.set_alpha(alpha_byte(alpha))
        self.screen.blit(image, (x + ox, y + oy))

    def _draw_hotcover(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """A tab's hotcover is always drawn at its hotrect: frame 1 while the
        tab is selected, else frame 0 (gameUI.dll 0x10004C98)."""
        frames = self.sprites.get(widget.attr("hotcover", "") or "") if widget.attr("hotcover", "") else None
        if not frames:
            return
        surface, offset_x, offset_y = frames[1 if widget.checked and len(frames) > 1 else 0]
        if surface is None:
            return
        hx, hy = widget.hot_rect[:2] if widget.hot_rect else (0, 0)
        surface.set_alpha(alpha_byte(alpha))
        self.screen.blit(surface, (x + hx + offset_x, y + hy + offset_y))
        surface.set_alpha(255)

    def _draw_items(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """List lines, newest at the bottom; older lines scroll off the top."""
        size = max(widget.text_size, 9)
        line_height = self.font(size).get_linesize()
        # value_channel_clientrect (35) is the text area inside the list.
        cx, cy, cw, ch = widget.values.get(35) or widget.caption_rect or (0, 0, widget.w, widget.h)
        lines: list[tuple[str, tuple[int, int, int]]] = []
        for text, color in widget.items:
            lines.extend((line, color) for line in self._wrap(text, size, max((cw or widget.w) - 16, 0)))
        visible = max((ch or widget.h) // line_height, 1)
        for number, (line, color) in enumerate(lines[-visible:]):
            rendered = self.text_line(line, size, color)
            rendered.set_alpha(alpha_byte(alpha))
            self.screen.blit(rendered, (x + cx + 2, y + cy + number * line_height))

    def _draw_glyph_frames(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """PICLABEL text uses one sprite frame per glyph (selRoom/num.img: digits, then '/')."""
        frames = self.sprites.get(widget.image) if widget.image else None
        if not frames:
            return
        advance = widget.text_width or widget.text_size
        for index, char in enumerate(widget.text):
            glyph = NUMBER_GLYPHS.find(char)
            if not 0 <= glyph < len(frames) or frames[glyph][0] is None:
                continue
            surface, offset_x, offset_y = frames[glyph]
            surface.set_alpha(alpha_byte(alpha))
            self.screen.blit(surface, (x + index * (advance or surface.get_width()) + offset_x, y + offset_y))
            surface.set_alpha(255)

    def _draw_number(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """NUMLABEL text is cut from a one-frame glyph strip (object/ui/common/number*.img).

        gameUI.dll 0x10018CFC: glyph i is the textwidth x textheight cell at
        x = i * textwidth, every character (drawn or not) advances textsize,
        and dt_center centres the whole string in the widget.
        """
        frames = self.sprites.get(widget.image) if widget.image else None
        if not frames or frames[0][0] is None:
            return
        strip = frames[0][0]
        width = widget.text_width or strip.get_width() // len(NUMBER_GLYPHS)
        height = widget.text_height or strip.get_height()
        advance = int(widget.attr("textsize", 0) or 0) or width
        if widget.text_style & DT_CENTER:
            x += (widget.w - len(widget.text) * advance) // 2
        strip.set_alpha(alpha_byte(alpha))
        for index, char in enumerate(widget.text):
            glyph = NUMBER_GLYPHS.find(char)
            if glyph >= 0:
                self.screen.blit(strip, (x + index * advance, y), pygame.Rect(glyph * width, 0, width, height))
        strip.set_alpha(255)

    def _draw_image(self, widget: Widget, x: int, y: int, alpha: float, *, angle: float | None = None) -> None:
        frames = self.sprites.get(widget.image)
        if not frames:
            return
        selected = widget.frame_range(len(frames))
        if selected is None:
            return
        first, last = selected
        index = first
        if last > first:
            interval = DEFAULT_FRAME_MS / widget.frame_speed if widget.frame_speed > 0 else DEFAULT_FRAME_MS
            elapsed = (time.monotonic() - self.started) * 1000
            index = first + int(elapsed / interval) % (last - first + 1)
        surface, offset_x, offset_y = frames[index]
        if surface is None:
            return
        if angle is not None:
            surface = remaining_angle_image(surface, angle)
        draw_x = x + offset_x + widget.image_offset[0]
        draw_y = y + offset_y + widget.image_offset[1]
        # gameUI.dll 0x10029A37: only bkImgFlag & 7 of 1 and 5 move the image,
        # by its canvas size (C division truncates toward zero).
        placement = widget.image_flag & 7
        if placement in (DT_CENTER, DT_CENTER | DT_VCENTER):
            canvas_w, canvas_h = self.sprites.canvas(widget.image)
            if placement == DT_CENTER:
                draw_x += int((widget.w - canvas_w) / 2)
                draw_y += int((widget.h - canvas_h) / 2)
            else:
                draw_y += widget.h - canvas_h
        opacity = alpha_byte(alpha)
        if opacity == 0:
            return
        surface.set_alpha(opacity)
        if widget.texture_rect is not None:
            tx, ty, tw, th = widget.texture_rect
            if tw > 0 and th > 0:
                self.screen.blit(surface, (draw_x + tx, draw_y + ty), pygame.Rect(tx, ty, tw, th))
        else:
            self.screen.blit(surface, (draw_x, draw_y))
        surface.set_alpha(255)

    def _draw_text(self, widget: Widget, x: int, y: int, alpha: float, focused: bool) -> None:
        text = "*" * len(widget.text) if widget.password() else widget.text
        if focused and widget.type in TEXT_INPUT_TYPES and int(time.monotonic() * 2) % 2 == 0:
            text += "|"
        if widget.caption_rect:
            cx, cy, cw, ch = widget.caption_rect
            box = pygame.Rect(x + cx, y + cy, cw or widget.w, ch or widget.h)
        else:
            box = pygame.Rect(x, y, widget.w, widget.h)
        if widget.type in TEXT_INPUT_TYPES and not widget.caption_rect:
            box = box.inflate(-4, 0)
        size = max(widget.text_size, 9)
        # A zero-width label breaks lines at zero bytes and shows nothing, like
        # every box narrower than one double-byte character (0x10021C74).
        if widget.type in WRAPPED_TYPES and box.width <= 0:
            return
        wrapped = widget.type in WRAPPED_TYPES and box.width > 0
        lines = self._wrap(text, size, box.width if wrapped else 0)
        # gameUI.dll 0x10021E86: lines are textsize + rowspace apart, and the
        # lines that do not fit the box are not drawn.
        rowspace = int(widget.attr("rowspace", 0) or 0)
        line_height = size + rowspace
        if wrapped and box.height > 0 and widget.type != "DYLABEL":
            lines = lines[:max((box.height + rowspace) // line_height, 0)]
        total = line_height * len(lines) - rowspace
        style = widget.text_style
        if widget.type in TEXT_INPUT_TYPES:
            style |= DT_VCENTER
        top = box.top
        if style & DT_VCENTER:
            top = box.top + (box.height - total) // 2
        elif style & DT_BOTTOM:
            top = box.bottom - total
        r, g, b, a = widget.draw_color
        color = (r, g, b)
        edge = widget.edge_color
        for number, line in enumerate(lines):
            rendered = self.text_line(line, size, color)
            left = box.left
            if style & DT_CENTER:
                left = box.left + (box.width - rendered.get_width()) // 2
            elif style & DT_RIGHT:
                left = box.right - rendered.get_width()
            position = (left, top + number * line_height + (size - rendered.get_height()) // 2)
            if edge[3] > 0:
                outline = self.text_line(line, size, edge[:3])
                outline.set_alpha(alpha_byte(alpha))
                for dx, dy in ((-1, 0), (1, 0), (0, -1), (0, 1)):
                    self.screen.blit(outline, (position[0] + dx, position[1] + dy))
            rendered.set_alpha(alpha_byte(alpha, a) if a else alpha_byte(alpha))
            self.screen.blit(rendered, position)

    def _draw_tip_box(self, widget: Widget, x: int, y: int, alpha: float) -> None:
        """DYLABEL grows with its text: the image's top and bottom thirds are
        drawn as they are and the middle third is stretched between them
        (0x1001132C slices at 1/3). The box spans the caption's margin above
        and below the text; that the margins are equal is inferred."""
        frames = self.sprites.get(widget.image) if widget.image else None
        if not frames or frames[0][0] is None:
            return
        image = frames[0][0]
        cx, cy, cw, _ = widget.caption_rect or (0, 0, widget.w, 0)
        size = max(widget.text_size, 9)
        rowspace = int(widget.attr("rowspace", 0) or 0)
        lines = self._wrap(widget.text, size, cw or widget.w)
        height = len(lines) * (size + rowspace) - rowspace + 2 * cy
        width, source_height = image.get_size()
        third = source_height // 3
        height = max(height, 2 * third)
        image.set_alpha(alpha_byte(alpha))
        self.screen.blit(image, (x, y), pygame.Rect(0, 0, width, third))
        middle = height - 2 * third
        if middle > 0:
            strip = image.subsurface(pygame.Rect(0, third, width, source_height - 2 * third))
            self.screen.blit(pygame.transform.scale(strip, (width, middle)), (x, y + third))
        self.screen.blit(image, (x, y + height - third), pygame.Rect(0, source_height - third, width, third))
        image.set_alpha(255)

    @staticmethod
    def _wrap(text: str, size: int, width: int) -> list[str]:
        """Break into lines of at most width // (size // 2) GBK bytes; a box
        narrower than one double-byte character shows nothing (0x10021C74)."""
        if 0 < width < 2 * (size // 2):
            return []
        lines: list[str] = []
        for paragraph in text.split("\n"):
            if width <= 0:
                lines.append(paragraph)
                continue
            line = ""
            for char in paragraph:
                if line and cell_width(line + char, size) > width:
                    lines.append(line)
                    line = char
                else:
                    line += char
            lines.append(line)
        return lines
