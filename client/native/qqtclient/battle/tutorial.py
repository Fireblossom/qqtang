"""新手任务: the lobby's 新手练习 → 新手任务, the first-login tutorial and the
three expert recordings that follow it.

Client.exe keeps the state on its game manager (0x00806A78):
+0x56EEF tutorial game, +0x56EED first tutorial (sc_firstBeginTutorial),
+0x56EFF playing the tutorial recordings, +0x56F00 how many were started.

- sc_beginTutorial (0x0048E690) sets +0x56EEF and asks QQTSection
  (0x1001C5A7) for a local GAME_BEGIN with only the local player on map 1501.
  The stages 1501..1504 (then 311) are run by Task.txt's controller (vtable
  0x007A2348), which the sidecar's tutorial mode ports; tutorial_stage.py
  draws them. See docs/tutorial-re.md.
- sc_firstBeginTutorial (0x0048E650) also sets +0x56EED. QQTSection runs it
  after the section login (0x1001BCD1) when the section mode is not 1 and
  the player has no win, loss or draw.
- The end (0x005A4181, also reached by leaving: 0x005A26C1 -> 0x005A2C6D)
  goes back to selRoom; after the first tutorial it runs NewPlayerPractice(),
  otherwise it asks 您是否需要观看高手玩家录像？ (handle 0x2000008). OK goes
  through OnMsgBoxResult (0x0048B410, module 2) to QQTSection 0x10023458
  case 8, which runs SC_PlayTutorialRecord().
- SC_PlayTutorialRecord (0x0048C960) plays tutorial1..3.qbv by the counter;
  each recording's end (0x005A2C6D, playback mode 3) starts the next while
  0 < counter < 3, then clears both and goes back to selRoom.
"""

from __future__ import annotations

import pathlib
from typing import Any, Callable

from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log

TUTORIAL_MAP = 1501  # QQTSection 0x1001C5A7 (0x5DD)
STAGE_MAPS = (1501, 1502, 1503, 1504)  # 0x005A4181: stages 0..3
RECORDS = ("tutorial1.qbv", "tutorial2.qbv", "tutorial3.qbv")  # 0x0048C960
ASK_RECORDS = (2 << 24) | 8  # module 2 (QQTSection 0x10023458), case 8
ASK_RECORDS_TEXT = "您是否需要观看高手玩家录像？"  # 0x007F2378
SECTION_MODE_NO_TUTORIAL = 1  # QQTSection +0xC768 == 1 (0x10029E3F) skips it
STAGE_FAILED_TEXT = "新手任务没能开始（对战规则程序不可用），按原版中途离开任务处理。"


def needs_first_tutorial(profile: Any) -> bool:
    """QQTSection 0x1001BCAF: after DealInitialize(), sc_firstBeginTutorial()
    runs unless the section mode is 1 or wins + losses + draws (the login
    reply's GameInfo at +0x33/+0x37/+0x3B) is at least 1."""
    if profile is None or int(profile.section_mode) == SECTION_MODE_NO_TUTORIAL:
        return False
    info = profile.game_info
    return info.wins + info.losses + info.draws < 1


def find_record(root: pathlib.Path, name: str) -> pathlib.Path:
    """Client.exe opens "tutorial1.qbv" relative to its directory; the files
    ship as Tutorial1.qbv, which only works on a case-insensitive disk."""
    path = root / name
    if path.is_file():
        return path
    return next((entry for entry in root.iterdir() if entry.name.lower() == name.lower()), path)


class TutorialFlow:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources):
        self.runtime, self.renderer, self.resources = runtime, renderer, resources
        self.tutorial = False  # +0x56EEF
        self.first = False  # +0x56EED
        self.records = False  # +0x56EFF
        self.counter = 0  # +0x56F00

    def _script(self, name: str, *args: Any) -> None:
        function = self.runtime.namespace.get(name)
        if callable(function):
            call_script(function, *args)

    # Starting -----------------------------------------------------------------

    def begin(self) -> None:
        """sc_beginTutorial (0x0048E690)."""
        self.tutorial = True
        self.start_stage()

    def first_begin(self) -> None:
        """sc_firstBeginTutorial (0x0048E650): +0x56EED = 1, then
        sc_beginTutorial()."""
        self.first = True
        self.begin()

    def begin_without_flag(self) -> None:
        """beginTutorial1 (0x0048E7F0) only asks QQTSection for the map 1501
        game (service +0x1B0) without setting +0x56EEF; no script calls it."""
        self.start_stage()

    def start_stage(self) -> None:
        """The local game on map 1501 with Task.txt's stages
        (tutorial_stage.TutorialBattle). When another activity (a battle,
        a recording) is running nothing starts and the flags are dropped
        again, so a later tutorial ends on the right branch. When the
        sidecar cannot start, the host says so and continues as the original
        does when the player leaves the tutorial at once (0x005A26C1 ->
        0x005A2C6D -> 0x005A4181)."""
        if self.runtime.activity is not None:
            log("[tutorial] another activity is running; the tutorial does not start")
            self.tutorial = self.first = False
            return
        from .tutorial_stage import TutorialBattle

        battle = TutorialBattle(self.runtime, self.renderer, self.resources, self.stage_over)
        try:
            battle.start()
        except Exception as error:  # noqa: BLE001 - a missing sidecar must not break the lobby
            log(f"[tutorial] cannot start map {TUTORIAL_MAP}: {error}")
            battle.close()

            def acknowledged(_ok: bool) -> None:
                self.runtime.post(self.leave)

            if not self.runtime.show_msgbox(STAGE_FAILED_TEXT, "", 0, "UI.SysMsgbox", acknowledged):
                self.runtime.post(self.leave)  # no message box: nothing would answer
            return
        self.runtime.activity = battle

    def stage_over(self, _ended: bool) -> None:
        """The local game ended (0xFBB, after the result) or was left: both
        go through 0x005A4181."""
        self.leave()

    # Ending -------------------------------------------------------------------

    def leave(self) -> None:
        """0x005A26C1: back to selRoom; a tutorial or free practice game then
        ends through 0x005A2C6D -> 0x005A4181."""
        if self.tutorial:
            self.over()
        else:
            self.runtime.goto_screen("selRoom")

    def over(self) -> None:
        """The tutorial branch of 0x005A4181 (+0x56EFF clear, +0x56F04 clear)."""
        self.tutorial = False
        if self.records:
            if 0 < self.counter < len(RECORDS):
                self.play_record()
            return
        self.runtime.goto_screen("selRoom")  # 0x005A3F12(1)
        if self.first:
            self.first = False
            self.runtime.post(lambda: self._script("NewPlayerPractice"))
        else:
            self.runtime.post(self.ask_records)

    def ask_records(self) -> None:
        """Service 0x0079DE58 +0x10 -> 0x0048B130: UI.SysMsgbox with handle
        0x2000008, then ui_msgBox(0) (确定/取消)."""
        self.runtime.show_msgbox(ASK_RECORDS_TEXT, "", 0, "UI.SysMsgbox", ASK_RECORDS)
        self._script("ui_msgBox", 0)

    def on_msgbox_result(self, handle: int, ok: int) -> bool:
        """QQTSection 0x10023458 case 8: OK runs SC_PlayTutorialRecord()."""
        if int(handle) != ASK_RECORDS:
            return False
        if ok:
            self.play_record()
        return True

    # Recordings ---------------------------------------------------------------

    def play_record(self) -> None:
        """SC_PlayTutorialRecord (0x0048C960)."""
        if self.counter == 0:
            self.records, self.counter = True, 0  # 0x00490BA0(1)
        if not 0 <= self.counter < len(RECORDS):
            return
        name = RECORDS[self.counter]
        self.counter += 1  # 0x00490BE0
        if self.runtime.activity is not None:
            # A recording that just ended is still the activity until the
            # main loop drops it.
            self.runtime.post(lambda: self._play(name))
        else:
            self._play(name)

    def _play(self, name: str) -> None:
        from .replay import ReplayBattle

        path = find_record(self.resources.root, name)
        try:
            self.runtime.activity = ReplayBattle(self.runtime, self.renderer, self.resources, path, self.record_done)
        except Exception as error:  # noqa: BLE001 - a missing file must not break the lobby
            log(f"[tutorial] cannot play {path}: {error}")
            self.records, self.counter = False, 0
            self.runtime.goto_screen("selRoom")

    def record_done(self) -> None:
        """0x005A2C6D in playback mode 3: the next recording while
        0 < counter < 3, otherwise back to selRoom."""
        if self.records and 0 < self.counter < len(RECORDS):
            for index in range(8):
                self._script("ui_NotifyShowLevelIcon", 0, index, 0)
            self.play_record()
            return
        self.records, self.counter = False, 0
        self.runtime.goto_screen("selRoom")  # 0x005A3F12(1)


def build_tutorial_natives(runtime: UIRuntime, renderer: Renderer, resources: Resources) -> tuple[TutorialFlow, dict[str, Callable[..., Any]]]:
    flow = TutorialFlow(runtime, renderer, resources)
    previous_result = runtime.namespace.get("OnMsgBoxResult")

    def OnMsgBoxResult(handle, ok):
        """0x0048B410 dispatches by handle >> 24; only module 2 case 8 is the
        tutorial's."""
        if flow.on_msgbox_result(handle, ok):
            return None
        return previous_result(handle, ok) if callable(previous_result) else None

    natives = {
        "sc_beginTutorial": flow.begin,
        "sc_firstBeginTutorial": flow.first_begin,
        "beginTutorial1": flow.begin_without_flag,
        "SC_PlayTutorialRecord": flow.play_record,
        "OnMsgBoxResult": OnMsgBoxResult,
    }
    return flow, natives
