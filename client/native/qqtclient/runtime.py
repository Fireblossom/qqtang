"""UI runtime: widget registry, screens, focus, timers and input dispatch."""

from __future__ import annotations

import queue
import time
from typing import Any, Callable

import pygame

from . import keylayout, scrollbar, textlist
from .script import ScriptLoader, call_script, log
from .widgets import TEXT_INPUT_TYPES, Widget, build
from .ui_alpha import advance_alpha

EDIT_NUMBER = 1
EDIT_CHAR_NUMBER = 4
# Win_SetValue channels used by fade effects (uiLogo): 41 sets the alpha step
# per widget update and 901 selects the direction (1 = fade in, 2 = fade out).
CHANNEL_ALPHA_SPEED = 41
CHANNEL_ALPHA = 42
CHANNEL_ALPHA_DIRECTION = 901
CHANNEL_ALPHA_MINIMUM = 904
FADE_IN, FADE_OUT = 1, 2
# Windows defaults: GetDoubleClickTime() and SM_CXDOUBLECLK / 2.
DOUBLE_CLICK_S = 0.5
DOUBLE_CLICK_PX = 2
DRAG_THRESHOLD_PX = 4
WGTSTYLE_POPUP = 0x400
VK_SHIFT, VK_CONTROL, VK_MENU = 16, 17, 18
# Widget classes whose input method (vtable +0x18) is 0x10004321 or calls it
# for a right button release: BUTTON, SPIN and MENUITEM directly, CHECK
# (0x10004A03) and CHANGEBAR (0x100082BE). They raise OnRBtnUp((x, y)).
RBTNUP_TYPES = ("BUTTON", "SPIN", "MENUITEM", "CHECK", "CHANGEBAR")


class UIRuntime:
    def __init__(self, loader: ScriptLoader):
        self.loader = loader
        self.namespace = loader.namespace
        self.root: Widget | None = None
        self.widgets: dict[str, Widget] = {}
        self.screens: dict[str, str] = {}
        self.current_screen: str | None = None
        self.names_flag = False  # shared F8 toggle: native keyboard +0x23
        self.focus: Widget | None = None
        self.delta_tick_ms = 0
        self._frame_widgets: list[Widget] | None = None
        self.pressed: Widget | None = None
        self.last_press: tuple[float, tuple[int, int], Widget | None] = (0.0, (0, 0), None)
        self.press_pos = (0, 0)
        self.mouse_pos = (0, 0)
        self.dragging: Widget | None = None
        # A moveable widget (a scroll bar's thumb) following the mouse, and
        # its top-left minus the press point (dispatcher +0x2B8, +0x70C/+0x710).
        self.moving: Widget | None = None
        self.move_offset = (0, 0)
        self.drag_image = ""  # Win_SetDragImg; drawn at the mouse while dragging
        # gameUI.dll counts the messages it dispatches ([0x10049928], read by
        # Win_GetEventCnt) and hides the open popup widget on a press outside it.
        self.event_count = 0
        self.popup: Widget | None = None
        self.hovered: Widget | None = None
        self.timers: dict[str, tuple[int, float]] = {}
        self.handler_paths: list[str] = []
        self.unknown_paths: set[str] = set()
        self.msgbox_target: tuple[str, Any] | None = None
        self.capture: Widget | None = None
        # A TEXTLIST's rows on screen for the name/link hit test, as
        # (left, top, height, textlist.Row); the entry point sets it to
        # Renderer.textlist_hit_rows, the layout drawing uses.
        self.textlist_rows: Callable[[Widget], list[Any]] = lambda widget: []
        self.main_thread_calls: queue.SimpleQueue[Callable[[], None]] = queue.SimpleQueue()
        self.periodic: dict[str, tuple[float, float, Callable[[], None]]] = {}
        self.quit_requested = False
        # A battle in progress: gets keyboard events and frame ticks from the
        # main loop (battle.practice.PracticeSession, battle.online.OnlineBattle).
        self.activity: Any = None
        # Host parts that run outside any widget (room avatar walking):
        # called with every input event and with each frame's elapsed ms.
        self.event_hooks: list[Callable[[Any], None]] = []
        self.tick_hooks: list[Callable[[int], None]] = []

    # Tree management -----------------------------------------------------

    def start(self, first_screen: str) -> None:
        self.loader.exec_file("res/uiRes/uiMain.pyc")
        ui_class = self.namespace["UI"]
        for entry in getattr(ui_class, "AllScreen", ()):
            self.screens[entry["id"]] = entry["path"]
        self.root = build("UI", ui_class, None)
        self.root.w, self.root.h = 800, 600
        self._register(self.root)
        self._init_tree(self.root)
        self.goto_screen(first_screen)

    def _register(self, widget: Widget) -> None:
        self._frame_widgets = None
        for item in widget.walk():
            self.widgets[item.path.lower()] = item

    def _unregister(self, widget: Widget) -> None:
        self._frame_widgets = None
        for item in widget.walk():
            self.widgets.pop(item.path.lower(), None)
            self.timers.pop(item.path.lower(), None)

    def find(self, path: Any) -> Widget | None:
        if not isinstance(path, str):
            return None
        widget = self.widgets.get(path.lower())
        if widget is None and path.lower() not in self.unknown_paths:
            self.unknown_paths.add(path.lower())
            log(f"[unknown widget] {path}")
        return widget

    def goto_screen(self, screen_id: str) -> None:
        assert self.root is not None
        script = self.screens.get(screen_id)
        if script is None:
            log(f"[unknown screen] {screen_id}")
            return
        if self.current_screen is not None:
            previous = self.find(f"UI.{self.current_screen}")
            if previous is not None:
                self.call(previous, "OnDenit")
                self.root.children.remove(previous)
                self._unregister(previous)
        ui_children = self.namespace["UI"].children
        if not hasattr(ui_children, screen_id):
            self.loader.exec_file(script)
        screen = build(f"UI.{screen_id}", getattr(ui_children, screen_id), self.root, len(self.root.children))
        self.root.children.append(screen)
        self._register(screen)
        self.current_screen = screen_id
        self.focus = None
        log(f"[screen] {screen_id}")
        self._init_tree(screen)

    def _init_tree(self, top: Widget) -> None:
        """OnInit for every widget under `top`, then for `top` itself.

        Seat widgets of uiRoom report their positions from their own OnInit
        (room_setPlayerPos) and the screen's OnInit relies on that.
        """
        for widget in top.walk():
            if widget is not top:
                self.call(widget, "OnInit")
        self.call(top, "OnInit")

    def screen_widget(self) -> Widget | None:
        return self.find(f"UI.{self.current_screen}") if self.current_screen else None

    # Script calls --------------------------------------------------------

    def call(self, widget: Widget, name: str, *args: Any) -> Any:
        handler = widget.handler(name)
        if handler is None:
            return None
        self.handler_paths.append(widget.path)
        try:
            return call_script(handler, *(args or (widget.path,)))
        finally:
            self.handler_paths.pop()

    # Timers and animation ------------------------------------------------

    def set_timer(self, path: str, interval_ms: int) -> None:
        widget = self.find(path)
        if widget is None:
            return
        key = widget.path.lower()
        if interval_ms <= 0:
            self.timers.pop(key, None)
        else:
            self.timers[key] = (int(interval_ms), time.monotonic() + interval_ms / 1000)

    def post(self, function: Callable[[], None]) -> None:
        """Run function on the UI thread; network threads use this to call scripts."""
        self.main_thread_calls.put(function)

    def every(self, name: str, seconds: float, function: Callable[[], None] | None) -> None:
        """Run function every `seconds` on the UI thread (None cancels), like the native refresh timers."""
        if function is None:
            self.periodic.pop(name, None)
        else:
            self.periodic[name] = (seconds, time.monotonic() + seconds, function)

    def tick(self, elapsed_ms: float) -> None:
        self.delta_tick_ms = min(int(elapsed_ms), 150)  # sc_getDeltaTick, 0x0042ECD0
        if self.focus is not None and self.focus.type == "KEYEDIT":
            keylayout.keyedit_tick(self.focus, elapsed_ms)
        # gameUI.dll runs each widget's OnFrame every frame (callback 56); the
        # in-game chat box slides up with it.
        if self._frame_widgets is None:
            self._frame_widgets = [widget for widget in self.widgets.values() if widget.handler("OnFrame")]
        for widget in self._frame_widgets:
            if widget.shown():
                self.call(widget, "OnFrame")
        while not self.main_thread_calls.empty():
            self.main_thread_calls.get()()
        now = time.monotonic()
        for name, (seconds, due, function) in list(self.periodic.items()):
            if now >= due and name in self.periodic:
                self.periodic[name] = (seconds, now + seconds, function)
                function()
        for key, (interval, due) in list(self.timers.items()):
            if now < due:
                continue
            # Reschedule first: the handler may stop or replace its own timer.
            self.timers[key] = (interval, now + interval / 1000)
            widget = self.widgets.get(key)
            if widget is not None:
                self.call(widget, "OnTimer")
        for widget in list(self.widgets.values()):
            widget.advance_animation(elapsed_ms)
            if widget.type == "DYMASK":
                # 0x10012057: degrees per second, approach destination.
                delta = widget.angle_step * elapsed_ms / 1000
                if widget.angle_cur < widget.angle_dest:
                    widget.angle_cur = min(widget.angle_dest, widget.angle_cur + delta)
                elif widget.angle_cur > widget.angle_dest:
                    widget.angle_cur = max(widget.angle_dest, widget.angle_cur - delta)
            if widget.type in ("SCROLLBAR", "SPIN"):
                # 0x10008B13 / 0x10004811: holding the track or a spin repeats.
                held = widget.pressed and self.pressed is widget and self.hovered is widget
                scrollbar.tick(widget, elapsed_ms / 1000, held, self.mouse_pos, self.call)
            if widget.type == "DYLIFESTRIP":
                # 10011D9A: approach destination at stepnum units/second.
                delta = widget.strip_step * elapsed_ms / 1000
                if widget.strip_current < widget.strip_destination:
                    widget.strip_current = min(widget.strip_destination, widget.strip_current + delta)
                else:
                    widget.strip_current = max(widget.strip_destination, widget.strip_current - delta)
            widget.alpha, widget.alpha_direction, disappeared = advance_alpha(
                widget.alpha, widget.alpha_speed, widget.alpha_minimum, widget.alpha_direction)
            if disappeared:
                self.call(widget, "OnDisappear")

    # Input ---------------------------------------------------------------

    def msgbox(self) -> Widget | None:
        box = self.widgets.get("ui.sysmsgbox")
        return box if box is not None and box.visible else None

    def modal_root(self) -> Widget | None:
        """The message box, or a widget holding the mouse capture while it is shown."""
        box = self.msgbox()
        if box is not None:
            return box
        if self.capture is not None and self.capture.shown() and self.capture.path.lower() in self.widgets:
            return self.capture
        return None

    def hit_test(self, position: tuple[int, int]) -> Widget | None:
        assert self.root is not None
        return self._hit(self.modal_root() or self.root, position)

    def _hit(self, widget: Widget, position: tuple[int, int]) -> Widget | None:
        """gameUI.dll's hit test (virtual +0xB4, base 0x1002912C).

        A hidden or disabled widget and its whole subtree are skipped, so the
        click falls through to the widgets below. Otherwise the topmost
        child wins; then the widget itself if the point is inside it. Any
        visible, enabled widget takes the click, handler or not; a Static
        one passes it to its nearest visible, enabled, non-Static ancestor.
        """
        if not widget.visible or not widget.enabled or widget.alpha <= 0:
            return None
        for child in reversed(widget.sorted_children()):
            found = self._hit(child, position)
            if found is not None:
                return found
        x, y, w, h = widget.hit_rect()
        if w <= 0 or h <= 0 or not pygame.Rect(x, y, w, h).collidepoint(position):
            return None
        if not widget.static():
            return widget
        # The walk (0x10027269) never ends at a screen: static containers
        # such as TPoser (a full-screen static by default, e.g. uiSelRoom's
        # chatArea over the mode tabs) let clicks through to their siblings.
        # Inferred: the screens' state flags were not traced.
        ancestor = widget.parent
        while ancestor is not None and (not ancestor.visible or not ancestor.enabled or ancestor.static()):
            ancestor = ancestor.parent
        if ancestor is not None and (ancestor.type == "SCREEN" or ancestor.parent is None):
            return None
        return ancestor

    def handle(self, event: pygame.event.Event) -> None:
        self.event_count += 1
        if event.type in (pygame.MOUSEBUTTONDOWN, pygame.MOUSEBUTTONUP):
            # A real button press always follows a WM_MOUSEMOVE to the same
            # point; scripted clicks (--click) do not move the mouse first.
            self.mouse_pos = event.pos
        if event.type == pygame.QUIT:
            self.quit_requested = True
        elif event.type == pygame.MOUSEMOTION:
            self.mouse_pos = event.pos
            if self.moving is not None:
                # 0x100304C8: while a widget is moved it gets event 0x19 and
                # nothing is hovered.
                ox, oy = self.move_offset
                scrollbar.move(self.moving, event.pos[0] + ox, event.pos[1] + oy, self.call)
                return
            self._hover(self.hit_test(event.pos))
            self._maybe_begin_drag(event.pos)
        elif event.type == pygame.MOUSEBUTTONDOWN and event.button == 1:
            self._close_popup(event.pos)
            target = self.hit_test(event.pos)
            self._hover(target)
            now = time.monotonic()
            last_time, last_pos, last_target = self.last_press
            self.last_press = (now, event.pos, target)
            # The second press of a double click arrives as WM_LBUTTONDBLCLK,
            # which gameUI.dll turns into OnDBClick instead of a press, so its
            # release clicks nothing.
            if (target is not None and target is last_target and now - last_time <= DOUBLE_CLICK_S
                    and abs(event.pos[0] - last_pos[0]) <= DOUBLE_CLICK_PX and abs(event.pos[1] - last_pos[1]) <= DOUBLE_CLICK_PX):
                self.last_press = (0.0, (0, 0), None)
                if target.enabled and target.type == "TEXTLIST":
                    self._textlist_press(target, event.pos, double=True)
                elif target.enabled:
                    self.call(target, "OnDBClick")
                return
            self.pressed = target
            self.press_pos = event.pos
            if target is not None:
                target.pressed = True
                # Any enabled widget takes focus when pressed, as native
                # controls do; buttons such as uiRoom's team colours find
                # their own index through Win_GetFocusPath.
                if target.enabled:
                    self.set_focus(target)
                if target.enabled and target.type == "TEXTLIST":
                    self._textlist_press(target, event.pos)
                if scrollbar.moveable(target):
                    tx, ty = target.absolute()
                    self.moving, self.move_offset = target, (tx - event.pos[0], ty - event.pos[1])
                elif target.type == "SCROLLBAR":
                    scrollbar.press_track(target, event.pos, self.call)
        elif event.type == pygame.MOUSEBUTTONUP and event.button == 1 and self.moving is not None:
            # The moved widget only gets the release; nothing is clicked.
            moved, self.moving = self.moving, None
            moved.pressed = False
            self.pressed = None
        elif event.type == pygame.MOUSEBUTTONUP and event.button == 1:
            target = self.hit_test(event.pos)
            pressed, self.pressed = self.pressed, None
            dragged, self.dragging = self.dragging, None
            self.drag_image = ""
            if pressed is not None:
                pressed.pressed = False
            if dragged is not None:
                if target is not None and target.enabled and target.drag_type:
                    self.call(target, "OnDragItemDrop")
                self.call(dragged, "OnEndItemDrag")
            elif pressed is not None and pressed is target and target.enabled and target.type != "TEXTLIST":
                # A TEXTLIST answers the press itself (0x10023F0A); its
                # release only clears the pressed state (0x1002ADEB case 7).
                self.click(target)
        elif event.type == pygame.MOUSEBUTTONDOWN and event.button == 3:
            # 0x100304C8 WM_RBUTTONDOWN: close a popup pressed outside, then
            # send input code 9 to the widget under the mouse. The base
            # method (0x1002ADEB) hides the widget's infowgt buddy (none is
            # declared by the scripts) and raises OnRClick; a TEXTLIST raises
            # OnRClickName instead (0x1002495A).
            self._close_popup(event.pos)
            target = self.hit_test(event.pos)
            if target is not None and target.enabled:
                if target.type == "TEXTLIST":
                    self._textlist_press(target, event.pos, right=True)
                else:
                    self.call(target, "OnRClick")
        elif event.type == pygame.MOUSEBUTTONUP and event.button == 3:
            self._right_release(event.pos)
        elif event.type == pygame.MOUSEWHEEL:
            # WM_MOUSEWHEEL goes to the focus, else the capture, else the
            # hovered widget, and bubbles up to a TEXTLIST (0x100304C8).
            target = self.focus or (self.capture if self.capture is not None and self.capture.shown() else None) or self.hovered
            scrollbar.wheel(target, int(event.y), self.call)
        elif event.type == pygame.TEXTINPUT:
            self._type_text(event.text)
        elif event.type == pygame.KEYDOWN:
            self._key(event)

    def _right_release(self, position: tuple[int, int]) -> None:
        """0x100304C8 WM_RBUTTONUP: input code 10 to the widget under the
        mouse; there is no press tracking. Button classes raise
        OnRBtnUp("(x,y)") (0x10004321, the "(%d,%d)" at 0x10046210), a
        KEYEDIT takes it as the right mouse key (0x10014AC9); the base
        method (0x1002ADEB default) passes the code to the parent, so a
        release over a button's static child still reaches the button."""
        widget = self.hit_test(position)
        if widget is None or not widget.enabled:
            return
        while widget is not None:
            if widget.type == "KEYEDIT":
                keylayout.keyedit_click(widget, right=True)
                return
            if widget.type in RBTNUP_TYPES:
                self.call(widget, "OnRBtnUp", (int(position[0]), int(position[1])))
                return
            widget = widget.parent

    def _textlist_press(self, widget: Widget, position: tuple[int, int], *, right: bool = False,
                        double: bool = False) -> None:
        """A TEXTLIST's own press handlers (textlist.py). Each first clears
        the list's text, which a name double click sets to the uin.

        - Left (0x10024190): on a link, OnClickHLink(url) and nothing else;
          on a name, OnClickName(name, uin) and then OnClick; on any other
          element, nothing; off every element, OnClick.
        - Right (0x1002495A): on a name, OnRClickName(name, uin).
        - Double (0x10024D10): on a name, the text becomes the uin and
          OnDBClick runs.
        """
        widget.text = ""
        hit = textlist.element_under(self.textlist_rows(widget), position)
        if hit is None:
            if not right and not double:
                self.call(widget, "OnClick")
            return
        row, index = hit
        if not right and not double:
            url = textlist.link_at(row.elements, index)
            if url is not None:
                self.call(widget, "OnClickHLink", url)
                return
        marker = textlist.name_at(row.elements, index)
        if marker is None:
            return
        name, uin = textlist.name_text(row.elements, marker), row.elements[marker].value
        if double:
            widget.text = str(uin)
            self.call(widget, "OnDBClick")
        elif right:
            self.call(widget, "OnRClickName", name, uin)
        else:
            self.call(widget, "OnClickName", name, uin)
            self.call(widget, "OnClick")

    def show(self, widget: Widget, shown: bool) -> None:
        if widget.visible != shown:
            widget.visible = shown
            if shown and widget.style & WGTSTYLE_POPUP:
                self.popup = widget
            self.call(widget, "OnSelfShow" if shown else "OnSelfHide")

    def _close_popup(self, position: tuple[int, int]) -> None:
        """0x10030916: a press outside the open popup hides it first."""
        popup = self.popup
        if popup is None or not popup.visible:
            return
        inside = self._hit(popup, position) is not None or pygame.Rect(popup.hit_rect()).collidepoint(position)
        if not inside:
            self.popup = None
            self.show(popup, False)

    def _accelerator(self, event: pygame.event.Event) -> bool:
        """The accel tables ((handler, virtual key, modifier key, ...)) of the
        focused widget, its ancestors and the screen. Cmd counts as Ctrl."""
        key = event.key
        if pygame.K_a <= key <= pygame.K_z:
            vk = ord("A") + key - pygame.K_a
        elif pygame.K_0 <= key <= pygame.K_9:
            vk = ord("0") + key - pygame.K_0
        elif pygame.K_F1 <= key <= pygame.K_F12:
            vk = 112 + key - pygame.K_F1
        else:
            return False
        mod = event.mod if hasattr(event, "mod") else 0
        modifier = VK_CONTROL if mod & (pygame.KMOD_CTRL | pygame.KMOD_META) else VK_SHIFT if mod & pygame.KMOD_SHIFT else VK_MENU if mod & pygame.KMOD_ALT else 0
        chain = []
        widget = self.focus or self.screen_widget()
        while widget is not None:
            chain.append(widget)
            widget = widget.parent
        for owner in chain:
            for entry in owner.attr("accel", ()) or ():
                if len(entry) >= 3 and int(entry[1]) == vk and int(entry[2]) == modifier and owner.handler(entry[0]):
                    self.call(owner, entry[0])
                    return True
        return False

    def _maybe_begin_drag(self, position: tuple[int, int]) -> None:
        """Widgets with a dragtype start a drag once the pressed mouse moves
        past the Windows drag threshold (SM_CXDRAG); the release then drops
        on the widget below instead of clicking. Which drag types accept
        which is not traced: any widget with a dragtype gets the drop."""
        source = self.pressed
        if source is None or self.dragging is not None or not source.drag_type or not source.enabled:
            return
        if max(abs(position[0] - self.press_pos[0]), abs(position[1] - self.press_pos[1])) <= DRAG_THRESHOLD_PX:
            return
        self.dragging = source
        self.call(source, "OnBeginItemDrag")

    def _hover(self, target: Widget | None) -> None:
        if target is self.hovered:
            return
        # A widget's tipwidget is shown only while the widget is hovered; the
        # scripts fill it in OnMouseMoveIn. Inferred from the tips starting
        # hidden (e.g. uiSelRoom tipShop); the native lookup is not traced.
        if self.hovered is not None:
            self.hovered.hover = False
            self.call(self.hovered, "OnMouseMoveOut")
            tip = self.find(self.hovered.attr("tipwidget", "") or None)
            if tip is not None:
                tip.visible = False
        self.hovered = target
        if target is not None:
            target.hover = True
            self.call(target, "OnMouseMoveIn")
            tip = self.find(target.attr("tipwidget", "") or None)
            if tip is not None:
                tip.visible = True

    def click(self, widget: Widget) -> None:
        box = self.msgbox()
        if box is not None and widget.parent is box:
            self._answer_msgbox(bool(widget.attr("defaultok", 0)))
            return
        if widget.type == "KEYEDIT":
            keylayout.keyedit_click(widget)
        if widget.type == "CHECK" and not widget.is_radio():
            widget.checked = 0 if widget.checked else 1
        else:
            self.select(widget)
        self.call(widget, "OnClick")
        if widget.type == "SPIN":
            scrollbar.spin_step(widget, self.call)  # 0x10004879: after OnClick

    def select(self, widget: Widget) -> None:
        """Radio checks and tabs (TABWIN) are exclusive among their siblings
        with the same groupid."""
        if not (widget.is_radio() or widget.type == "TABWIN"):
            return
        if widget.parent is not None:
            for sibling in widget.parent.children:
                same_kind = sibling.is_radio() if widget.is_radio() else sibling.type == "TABWIN"
                if sibling is not widget and same_kind and sibling.group_id == widget.group_id:
                    sibling.checked = 0
        widget.checked = 1

    def set_focus(self, widget: Widget | None) -> None:
        # ui_setCapture shows a dialog and focuses it after the calling
        # script explicitly focused one of its edits. Preserve that edit;
        # capture and keyboard focus are independent. This repairs the
        # original script's parent-focus overwrite (e.g. Add Friend).
        current = self.focus
        if current is not None and current is not widget and current.type == "KEYEDIT":
            current.key_capturing = False  # 0x10014AC9 event 15
        if (widget is not None and current is not None and current.type in TEXT_INPUT_TYPES
                and current.editable and current.enabled and current.shown()
                and current in widget.walk()):
            return
        self.focus = widget

    def _bubble_key(self, name: str) -> None:
        modal = self.modal_root()
        widget = self.focus
        if modal is not None and (widget is None or widget not in modal.walk()):
            widget = modal
        seen = set()
        while widget is not None:
            seen.add(widget)
            if widget.handler(name):
                self.call(widget, name)
                return
            if widget is modal:
                return
            widget = widget.parent
        screen = self.screen_widget()
        if screen is not None and screen not in seen:
            self.call(screen, name)

    def _type_text(self, text: str, widget: Widget | None = None) -> None:
        widget = widget or self.focus
        if widget is None or widget.type not in TEXT_INPUT_TYPES or not widget.editable:
            return
        if widget.edit_method in (EDIT_NUMBER, EDIT_CHAR_NUMBER):
            text = "".join(char for char in text if char.isdigit())
        if widget.max_char:
            text = text[: max(0, widget.max_char - len(widget.text))]
        widget.text += text

    def _key(self, event: pygame.event.Event) -> None:
        if self._accelerator(event):
            return
        widget = self.focus
        if widget is not None and widget.type == "KEYEDIT" and widget.enabled and widget.shown():
            # 0x1001491F answers every key itself, so a focused box keeps
            # Enter and Escape from the dialog.
            keylayout.keyedit_key(widget, event.key)
            return
        if event.key == pygame.K_BACKSPACE and widget is not None and widget.type in TEXT_INPUT_TYPES and widget.editable:
            widget.text = widget.text[:-1]
        elif event.key == pygame.K_TAB and widget is not None:
            if widget.handler("OnTab"):
                self.call(widget, "OnTab")
            else:
                self._next_edit(widget)
        elif event.key in (pygame.K_RETURN, pygame.K_KP_ENTER):
            if self.msgbox() is not None:
                self._answer_msgbox(True)
            else:
                self._bubble_key("OnEnter")
        elif event.key == pygame.K_ESCAPE:
            if self.msgbox() is not None:
                self._answer_msgbox(False)
            else:
                self._bubble_key("OnEscape")

    def _next_edit(self, widget: Widget) -> None:
        screen = self.modal_root() or self.screen_widget()
        if screen is None:
            return
        edits = [item for item in screen.walk() if item.type in TEXT_INPUT_TYPES and item.shown() and item.enabled]
        if edits:
            index = edits.index(widget) if widget in edits else -1
            self.set_focus(edits[(index + 1) % len(edits)])

    # Message box ---------------------------------------------------------

    def show_msgbox(self, text: str, caption: str, kind: int, target: str, handle: Any) -> bool:
        """False when there is no UI.SysMsgbox (the answer never comes)."""
        box = self.widgets.get("ui.sysmsgbox")
        if box is None:
            log(f"[msgbox] {text}")
            return False
        label = self.widgets.get("ui.sysmsgbox.textlabel")
        if label is not None:
            label.text = str(text)
        no_button = self.widgets.get("ui.sysmsgbox.nobtn")
        if no_button is not None:
            # Kind 0 is a notice with a single confirmation button.
            no_button.visible = kind != 0
        box.visible = True
        self.msgbox_target = (target, handle)
        return True

    def _answer_msgbox(self, ok: bool) -> None:
        box = self.widgets.get("ui.sysmsgbox")
        if box is not None:
            box.visible = False
        target_path, handle = self.msgbox_target or ("UI.SysMsgbox", -1)
        self.msgbox_target = None
        if callable(handle):
            handle(ok)  # a host-side question (e.g. a room invitation)
            return
        target = self.find(target_path)
        if target is not None:
            self.call(target, "OnMsgBoxOk" if ok else "OnMsgBoxCancel", handle)
