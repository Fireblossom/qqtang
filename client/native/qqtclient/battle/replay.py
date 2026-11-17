"""Watching a .qbv recording (菜单 → 录像), and saving one (保存录像).

Playback (Client.exe PlayRecord 0x0048C910 -> 0x005A36CE) rebuilds the match
from the file's GAME_BEGIN and plays every recorded battle message at its
scene time; the sidecar's replay mode is a spectator that takes all traffic
from the file. The panel's buttons set the player object's state: pause
(+0x3B3894 = 1, 0x005A3F76), resume at normal speed (= 2, speed 1,
0x005A3F9D) and x2/x4 (= 2, speed n, 0x005A3FD6). The recording's GAME_OVER
gives the result screen, after which the previous screen returns.

Recording: Client.exe keeps every live match and writes
Record/<QQ号>/<年>年<月>月<日>日<时>时<分>分.qbv (0x005A35FB) when the match ends
with 保存录像 on, or at once if it is pressed after the end; 取消保存录像
(StopRecord) turns it off. Its hint text function 0x005C81D7 is empty, so no
message is shown.
"""

from __future__ import annotations

import datetime
import pathlib
from typing import Any, Callable

import pygame

from .. import qbv
from ..avatar import AvatarCache
from ..natives import from_windows_path
from ..net import match as match_net
from ..net import room as room_net
from ..net.player import GameInfo
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log
from .online import RESULT_SHOWN_MS, SKIP_KEYS, MatchMember, result_rows
from .scene import BattleScene
from .sidecar import Sidecar

RECORD_DIR = "Record"
RESULT_DELAY_MS = 1000  # after the last recorded message
DRIVE_Z = 1 << 25  # the host exposes the file system as Z: (natives.to_windows_path)


class ReplaySidecar(Sidecar):
    """Steps the sidecar's replay mode to the playback clock; the scene's
    actions and TCP forwards do not apply."""

    def __init__(self) -> None:
        super().__init__()
        self.clock_ms = 0.0

    def start_replay(self, client_root: pathlib.Path, path: pathlib.Path) -> dict[str, Any]:
        return self._call({"cmd": "init", "client_root": str(client_root), "online": {"replay": str(path)}})

    def step(self, actions: list[dict[str, Any]], ticks: int = 1, tcp: list[tuple[int, bytes]] | None = None) -> dict[str, Any]:
        return self._call({"cmd": "step", "ticks": ticks, "actions": [], "replay_ms": int(self.clock_ms)})


def recorded_wear(member: room_net.RoomPlayer, role: int, namespace: dict[str, Any]) -> tuple[tuple[str, int], ...]:
    """The parts the room would dress: items worn by the role, through the
    script's itemList/equipMap as useCommItem does."""
    items, equip = namespace.get("itemList") or {}, namespace.get("equipMap") or {}
    wear: dict[str, int] = {}
    for item in member.items:
        description = items.get(item.item_id) if item.role_id == role else None
        if description and (description[0] in equip or description[0] in ("bomb", "huanying", "footprint")):
            wear[str(description[0])] = int(description[1])
    return tuple(sorted(wear.items()))


class ReplayBattle:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources, path: pathlib.Path,
                 back: Callable[[], None]):
        self.runtime, self.renderer, self.back = runtime, renderer, back
        self.recording = qbv.read(path)
        sidecar = ReplaySidecar()
        try:
            state = sidecar.start_replay(resources.root, path)
        except Exception:
            sidecar.close()
            raise
        self.sidecar = sidecar
        begin_info = state["begin"]
        members = {member.player_id: member for member in self.recording.players}
        players = []
        for player in begin_info["players"]:
            member = members.get(player["id"])
            players.append(dict(player, name=member.nickname.strip() if member else f"玩家{player['id']}",
                                seat=member.seat - 1 if member else -1, points=member.game_info.points if member else 0,
                                gender=member.gender if member else 0))
        wear = {player["id"]: recorded_wear(members[player["id"]], player["role"], runtime.namespace)
                for player in players if player["id"] in members}
        self.members = {player_id: MatchMember(member.uin, member.nickname, member.game_info.points, wear.get(player_id, ()),
                                               member.seat - 1, member.gender, member.identity)
                        for player_id, member in members.items()}
        log(f"[replay] {path.name}: map {begin_info['map_id']}, {len(self.recording.events)} messages, "
            f"{self.recording.duration_ms} ms, recorded by {self.recording.local_uin}")
        self.scene = BattleScene(resources, renderer.sprites, AvatarCache(resources), sidecar, begin_info["map_id"], players,
                                 local_id=self.recording.local_id, state=state, realtime=True, wear=wear)
        self.paused, self.speed = False, 1
        self.elapsed_ms = 0.0
        self.finish_at: float | None = None
        self.finished = False
        runtime.namespace.update({
            "LeaveGame": self.leave,
            "GetPVEBossInfo": lambda: [],
            "GetPVEPlayerInfo": lambda: [],
        })
        runtime.goto_screen("game")
        self.scene.attach(runtime, begin_info["map_id"], renderer.font, renderer.text_line, renderer.draw_widget)
        renderer.underlay, renderer.overlay = self.scene.draw, self.scene.draw_overlay
        # 0x005A3C7F: hide both record controls, then show the player panel.
        self._script("showRecordRecorder", 0)
        self._script("showRecordPlayer", 1)
        self._script("setRecordQQNumber", self.recording.local_uin)

    def _script(self, name: str, *args: Any) -> None:
        function = self.runtime.namespace.get(name)
        if callable(function):
            call_script(function, *args)

    def pause(self) -> None:
        self.paused = True

    def resume(self) -> None:
        self.paused, self.speed = False, 1

    def set_speed(self, speed: int) -> None:
        self.paused = False
        if speed > 0:
            self.speed = speed

    def handle(self, event: pygame.event.Event) -> None:
        if self.finished:
            return
        if self.finish_at is not None and (event.type == pygame.KEYDOWN and event.key in SKIP_KEYS
                                           or event.type == pygame.MOUSEBUTTONDOWN):
            self.finish()
            return
        if event.type in (pygame.KEYDOWN, pygame.KEYUP) and event.key == pygame.K_F8:
            self.scene.handle(event)  # name plates; nobody is controlled

    def tick(self, elapsed_ms: float) -> None:
        if self.finished:
            return
        self.elapsed_ms += elapsed_ms
        if not self.paused:
            self.sidecar.clock_ms += elapsed_ms * self.speed
        self.scene.tick(elapsed_ms)
        if self.finish_at is None and self.sidecar.clock_ms >= self.recording.duration_ms + RESULT_DELAY_MS:
            self.show_results()
        if self.finish_at is not None and self.elapsed_ms >= self.finish_at:
            self.finish()

    def show_results(self) -> None:
        self.finish_at = self.elapsed_ms + RESULT_SHOWN_MS
        if not self.recording.over:
            return
        try:
            over = match_net.parse_game_over(self.recording.over)
        except Exception as error:  # noqa: BLE001 - a damaged result still ends the replay
            log(f"[replay] unreadable GAME_OVER: {error}")
            return
        mine = next((result for result in over.results if result.player_id == self.recording.local_id), None)
        self.scene.result = mine.result if mine is not None else match_net.RESULT_LOSS
        self.scene.over = True
        self.scene.show_results(result_rows(over, self.members, self.scene.map.rule))

    def leave(self) -> None:
        self.finish()

    def finish(self) -> None:
        if self.finished:
            return
        self.close()
        self.back()

    def close(self) -> None:
        if self.finished:
            return
        self.finished = True
        self.scene.close()
        if self.renderer.underlay == self.scene.draw:
            self.renderer.underlay = self.renderer.overlay = None


# Saving ------------------------------------------------------------------------


def record_path(root: pathlib.Path, uin: int, now: datetime.datetime | None = None) -> pathlib.Path:
    """Client.exe's "Record/%u/%d年%d月%d日%d时%d分.qbv" (seconds unused)."""
    now = now or datetime.datetime.now()
    return root / RECORD_DIR / str(uin) / f"{now.year}年{now.month}月{now.day}日{now.hour}时{now.minute}分.qbv"


def ai_record(player: dict[str, Any]) -> bytes:
    """A PLAYER_INFO_IN_ROOM_OLD for a player the room did not list (a
    server AI): its PlayerID, team and role, nothing worn."""
    info = GameInfo(*([0] * 13))
    info.role_id = int(player.get("role", 1))
    member = room_net.RoomPlayer(uin=0, nickname=str(player.get("name", "电脑")), player_id=int(player["id"]),
                                 team=int(player.get("team", 1)), seat=max(int(player.get("seat", -1)) + 1, 1), status=0,
                                 gender=0, icon_id=0, identity=0, game_info=info, items=[], kin_index=0, kin_name="",
                                 kin_flag=b"\xff" * 8, pet=bytes(room_net.PET_INFO_MINIMUM))
    return room_net.encode_room_player(member)


def save_recording(path: pathlib.Path, sidecar: Sidecar, begin: bytes, over: bytes, local_uin: int, local_id: int,
                   players: list[dict[str, Any]], records: dict[int, bytes]) -> None:
    """begin is the length-prefixed GameBegin payload the match started with."""
    reply = sidecar._call({"cmd": "record"})
    events = [qbv.Event(entry["t"], entry["s"], bytes.fromhex(entry["d"]), tuple(entry["o"]))
              for entry in reply.get("recorded") or ()]
    recording = qbv.Recording(local_uin, local_id, begin[2:], over, [], events)
    qbv.write(path, recording, [records.get(player["id"]) or ai_record(player) for player in players])
    log(f"[replay] saved {path} ({len(events)} messages)")


# Natives -------------------------------------------------------------------------


def build_replay_natives(runtime: UIRuntime, renderer: Renderer, resources: Resources) -> dict[str, Callable[..., Any]]:
    def current() -> Any:
        return runtime.activity

    def PlayRecord(path):
        if runtime.activity is not None:
            return
        file = from_windows_path(str(path))
        screen = runtime.current_screen or "selRoom"

        def back() -> None:
            runtime.goto_screen(screen)

        try:
            runtime.activity = ReplayBattle(runtime, renderer, resources, file, back)
        except Exception as error:  # noqa: BLE001 - a bad file must not break the lobby
            log(f"[replay] cannot play {file}: {error}")
            runtime.show_msgbox(f"无法播放这个录像：{error}", "", 0, "UI.SysMsgbox", 0)

    def PausePlayRecord():
        if isinstance(current(), ReplayBattle):
            current().pause()

    def ResumePlayRecord():
        if isinstance(current(), ReplayBattle):
            current().resume()

    def SetPlayRecordSpeed(speed):
        if isinstance(current(), ReplayBattle):
            current().set_speed(int(speed))

    def BeginRecord():
        activity = current()
        if hasattr(activity, "set_recording"):
            activity.set_recording(True)

    def StopRecord():
        activity = current()
        if hasattr(activity, "set_recording"):
            activity.set_recording(False)

    def SC_ClickWatchVedioBtn():
        pass  # statistics counter (0x00531240)

    def win32_GetLogicalDrives():
        return DRIVE_Z

    return {name: value for name, value in locals().items() if name[0].isupper() or name.startswith("win32_")}
