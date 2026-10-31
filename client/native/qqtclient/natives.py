"""Native functions the original host injected into the script namespace.

Each implementation follows how the scripts use the call; channel numbers are
the value_channel_* constants from uiConst. Anything not defined here falls
through to a MissingNative stub and appears in the missing-native report.
"""

from __future__ import annotations

import ast
import pathlib
from typing import Any, Callable

import pygame

from .resources import Resources
from .runtime import CHANNEL_ALPHA_DIRECTION, CHANNEL_ALPHA_SPEED, UIRuntime
from .script import log
from .widgets import TEXT_INPUT_TYPES, Widget

CHANNEL_VALUE = 10
CHANNEL_MAXVALUE = 12
CHANNEL_POS = 17
CHANNEL_WINRECT = 34
CHANNEL_CAPTIONRECT = 36
CHANNEL_CAPTION = 200
CHANNEL_LISTITEM_NUM = 32
CHANNEL_ITEMTEXT = 207
MAX_LIST_ITEMS = 200


def to_windows_path(path: pathlib.Path) -> str:
    """Scripts manipulate Windows paths; expose the file system as drive Z: like Wine."""
    return "Z:" + str(path).replace("/", "\\")


def from_windows_path(path: str) -> pathlib.Path:
    if len(path) >= 2 and path[1] == ":":
        path = path[2:]
    return pathlib.Path(path.replace("\\", "/") or "/")


def load_device_config(path: pathlib.Path) -> type:
    """Parse devConfig.txt ("class device:" followed by "key= literal" lines) without executing it."""
    values: dict[str, Any] = {}
    if path.is_file():
        for line in path.read_text(encoding="gbk", errors="replace").splitlines()[1:]:
            key, separator, value = line.partition("=")
            if separator:
                try:
                    values[key.strip()] = ast.literal_eval(value.strip())
                except (ValueError, SyntaxError):
                    log(f"[devConfig] ignoring {line!r}")
    return type("device", (), values)


class Audio:
    def __init__(self, resources: Resources):
        self.resources = resources
        self.sounds: dict[str, pygame.mixer.Sound | None] = {}
        self.sound_enabled = True
        try:
            pygame.mixer.init()
            self.available = True
        except pygame.error as error:
            log(f"[audio] disabled: {error}")
            self.available = False

    def play_sound(self, name: str, loops: int = 1) -> None:
        if not self.available or not self.sound_enabled:
            return
        if name not in self.sounds:
            path = self.resources.path(f"sound/{name}")
            self.sounds[name] = pygame.mixer.Sound(str(path)) if path else None
            if path is None:
                log(f"[missing sound] {name}")
        sound = self.sounds[name]
        if sound is not None:
            sound.play(loops=max(0, loops - 1) if loops >= 0 else -1)

    def play_music(self, name: str, loops: int = -1) -> None:
        path = self.resources.path(f"music/{name}")
        if not self.available or path is None:
            return
        pygame.mixer.music.load(str(path))
        pygame.mixer.music.play(loops=loops)


def build_natives(runtime: UIRuntime, resources: Resources, audio: Audio) -> dict[str, Callable[..., Any]]:
    find = runtime.find

    def with_widget(default: Any = None) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        def wrap(function: Callable[..., Any]) -> Callable[..., Any]:
            def call(path: Any, *args: Any) -> Any:
                widget = find(path)
                return default if widget is None else function(widget, *args)
            call.__name__ = function.__name__
            return call
        return wrap

    @with_widget()
    def Win_ShowWidget(widget, show=1):
        runtime.show(widget, bool(show))

    @with_widget(0)
    def Win_IsVisible(widget):
        return int(widget.visible)

    @with_widget()
    def Win_EnableWidget(widget, enable=1):
        widget.enabled = bool(enable)

    @with_widget(0)
    def Win_IsEnabled(widget):
        return int(widget.enabled)

    def add_item(widget, text, color) -> None:
        widget.items.append((str(text), tuple(int(value) for value in color[:3])))
        del widget.items[:-MAX_LIST_ITEMS]

    @with_widget()
    def Win_SetText(widget, text, channel=CHANNEL_CAPTION):
        if channel == CHANNEL_CAPTION:
            widget.text = "" if text is None else str(text)
        elif channel == CHANNEL_ITEMTEXT:
            add_item(widget, text, widget.draw_color)
        elif channel == CHANNEL_LISTITEM_NUM:
            if not text:
                widget.items.clear()
        else:
            widget.values[channel] = text

    @with_widget()
    def Win_SetColorText(widget, text, channel, r, g, b):
        if channel == CHANNEL_ITEMTEXT:
            add_item(widget, text, (r, g, b))
        else:
            Win_SetText(widget.path, text, channel)

    @with_widget("")
    def Win_GetText(widget, channel=CHANNEL_CAPTION):
        return widget.text if channel == CHANNEL_CAPTION else widget.values.get(channel, "")

    @with_widget()
    def Win_SetImg(widget, image):
        widget.image = image or None

    @with_widget()
    def Win_SetStripNum(widget, value):
        widget.strip_current = float(value)

    @with_widget()
    def Win_SetStripTotalNum(widget, value):
        widget.strip_total = float(value)

    @with_widget()
    def Win_SetStripDestNum(widget, value):
        widget.strip_destination = float(value)

    @with_widget()
    def Win_SetStripStepNum(widget, value):
        widget.strip_step = float(value)

    def Win_SetDragImg(_path, image):
        runtime.drag_image = str(image or "")

    def Win_GetEventCnt():
        return runtime.event_count

    def clipboard_text() -> str:
        try:
            return pygame.scrap.get_text() or ""
        except pygame.error:
            return ""

    def set_clipboard(text: str) -> None:
        try:
            pygame.scrap.put_text(text)
        except pygame.error:
            pass

    def editable_focus() -> Widget | None:
        widget = runtime.focus
        return widget if widget is not None and widget.type in TEXT_INPUT_TYPES else None

    def Win_FocusOnCopy():
        widget = editable_focus()
        if widget is not None and not widget.password():
            set_clipboard(widget.text)

    def Win_FocusOnCut():
        widget = editable_focus()
        if widget is not None and widget.editable and not widget.password():
            set_clipboard(widget.text)
            widget.text = ""

    def Win_FocusOnPaste():
        runtime._type_text(clipboard_text().replace("\r", "").split("\n")[0])

    def Win_FocusOnInsert(path, text):
        runtime._type_text(str(text), find(path))

    @with_widget()
    def Win_SetValue(widget, value, channel=CHANNEL_VALUE):
        if channel == CHANNEL_ALPHA_SPEED:
            widget.alpha_speed = float(value)
        elif channel == CHANNEL_ALPHA_DIRECTION:
            widget.alpha_direction = int(value)
        widget.values[channel] = value

    @with_widget(0)
    def Win_GetValue(widget, channel=CHANNEL_VALUE):
        return widget.values.get(channel, 0)

    @with_widget()
    def Win_SetCheck(widget, checked):
        widget.checked = 1 if checked else 0

    @with_widget(0)
    def Win_IsChecked(widget):
        return widget.checked

    def Win_SetFocus(path=None):
        runtime.set_focus(find(path) if path else None)

    def Win_SetCapture(path=""):
        runtime.capture = find(path) if path else None

    def Win_GetFocusPath():
        return runtime.focus.path if runtime.focus else ""

    def Win_Timer(path, interval_ms):
        runtime.set_timer(path, int(interval_ms))

    @with_widget()
    def Win_SelectSelf(widget):
        runtime.select(widget)

    @with_widget()
    def Win_SetDrawColor(widget, r, g, b, a):
        widget.draw_color = (r, g, b, a)

    @with_widget()
    def Win_SetBkColor(widget, r, g, b, a):
        widget.back_color = (r, g, b, a)

    @with_widget()
    def Win_SetEdgeColor(widget, r, g, b, a):
        widget.edge_color = (r, g, b, a)

    def Win_GetCurScreen():
        return runtime.current_screen or ""

    def Win_GetMyPath():
        return runtime.handler_paths[-1] if runtime.handler_paths else ""

    # Positions passed to and from the scripts are screen coordinates
    # (uiRoom.chatFlexArea moves chat panels to absolute positions inside
    # their parent); widgets store them relative to the parent.
    def to_parent(widget, x, y) -> tuple[int, int]:
        parent_x, parent_y = widget.parent.absolute() if widget.parent is not None else (0, 0)
        return int(x) - parent_x, int(y) - parent_y

    @with_widget()
    def Win_Move2Pos(widget, x, y):
        widget.x, widget.y = to_parent(widget, x, y)

    @with_widget()
    def Win_MovePos(widget, dx, dy):
        widget.x += int(dx)
        widget.y += int(dy)

    @with_widget()
    def Win_SetRect(widget, channel, x, y, w, h):
        if channel == CHANNEL_WINRECT:
            (widget.x, widget.y), widget.w, widget.h = to_parent(widget, x, y), int(w), int(h)
        elif channel == CHANNEL_CAPTIONRECT:
            widget.caption_rect = (int(x), int(y), int(w), int(h))
        else:
            widget.values[channel] = (x, y, w, h)

    @with_widget((0, 0, 0, 0))
    def Win_GetRect(widget, channel=CHANNEL_WINRECT):
        if channel == CHANNEL_WINRECT:
            return (*widget.absolute(), widget.w, widget.h)
        if channel == CHANNEL_CAPTIONRECT:
            return widget.caption_rect or (0, 0, widget.w, widget.h)
        return widget.values.get(channel, (0, 0, widget.w, widget.h))

    @with_widget()
    def Win_SetDrawTexRect(widget, x, y, w, h):
        widget.texture_rect = (int(x), int(y), int(w), int(h))

    @with_widget()
    def Win_SetDrawWinRect(widget, x, y, w, h):
        widget.texture_rect = (int(x), int(y), int(w), int(h))

    @with_widget(0)
    def Win_GetX(widget):
        return widget.absolute()[0]

    @with_widget(0)
    def Win_GetY(widget):
        return widget.absolute()[1]

    @with_widget()
    def Win_SetPos(widget, position):
        widget.values[CHANNEL_POS] = position

    @with_widget(0)
    def Win_GetPos(widget):
        return widget.values.get(CHANNEL_POS, 0)

    @with_widget()
    def Win_SetScrollPos(widget, position):
        widget.values[CHANNEL_POS] = int(position)

    @with_widget(0)
    def Win_GetScrollPos(widget):
        return int(widget.values.get(CHANNEL_POS, 0))

    @with_widget()
    def Win_SetRange(widget, maximum):
        widget.values[CHANNEL_MAXVALUE] = int(maximum)

    @with_widget()
    def Win_Update(widget):
        pass

    def Win_ShowMsgBox(text, caption="", kind=0, target="UI.SysMsgbox", handle=-1):
        runtime.show_msgbox(text, caption, kind, target, handle)

    def Win_ConsolePrint(text):
        text = str(text)
        if text.strip():
            log(f"[script] {text.rstrip()}")

    def GotoUIScreen(screen_id):
        runtime.goto_screen(screen_id)

    def MyExecFile(path):
        runtime.loader.exec_file(path)

    def os_getmoduledir():
        return to_windows_path(resources.root)

    def os_isdir(path):
        return int(from_windows_path(str(path)).is_dir())

    def os_listdir(path):
        directory = from_windows_path(str(path))
        return sorted(child.name for child in directory.iterdir()) if directory.is_dir() else []

    def PlaySound(name, loops=1):
        audio.play_sound(name, loops)

    def PlayMusic(name, loops=-1):
        if runtime.namespace["device"].music:
            audio.play_music(name, loops)

    def PauseSound():
        audio.sound_enabled = False

    def ResumeSound():
        audio.sound_enabled = True

    def PauseMusic():
        if audio.available:
            pygame.mixer.music.pause()

    def ResumeMusic():
        if audio.available:
            pygame.mixer.music.unpause()

    def Quit():
        runtime.quit_requested = True

    def NotifyMainWndReady(delay_ms=0):
        pass

    def GetLoginUinFromCmdLn():
        return 0

    def CanLoginFromVNet():
        return 0

    natives = {name: value for name, value in locals().items() if callable(value) and (name[0].isupper() or name.startswith("os_"))}
    natives["device"] = load_device_config(resources.root / "devConfig.txt")
    return natives
