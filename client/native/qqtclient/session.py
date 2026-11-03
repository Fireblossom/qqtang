"""Client session state and the network-facing natives (login, sections).

Login order, as the server expects it:
  1. QQTAUTH1 password proof on TCP 18000 (grants this host + UIN);
  2. directory discovery 0x0133 on TCP 18080 (turns the grant into a section
     login window and returns the channel/section list);
  3. REQUEST_LOGIN 0x0064 on the chosen section's game server, which stays
     connected for the lobby.
"""

from __future__ import annotations

import json
import os
import pathlib
import random
import threading
from typing import Any, Callable

from .net import accountauth, directory, game, player
from .runtime import UIRuntime
from .script import call_script, log

# A second local server (for tests running side by side) can be reached
# through QQT_AUTH_PORT and QQT_DIRECTORY_PORT.
ACCOUNT_AUTH_PORT = int(os.environ.get("QQT_AUTH_PORT", "18000"))
DIRECTORY_PORT = int(os.environ.get("QQT_DIRECTORY_PORT", "18080"))
DEFAULT_ROLE_ID = 1
STATE_FILE = pathlib.Path(os.path.expanduser("~/Library/Application Support/QQTangNative/state.json"))


class Session:
    def __init__(self, server_host: str):
        self.server_host = server_host
        # Online matches play the local player with the sidecar's test bot
        # (--autoplay) instead of the keyboard.
        self.autoplay = False
        # Players whose whispers are shielded (/M, QQTSection +0xC7B4): a
        # client-side list kept for the session, never sent to the server.
        self.shielded: set[int] = set()
        # The current room for slash commands (room.py sets it).
        self.room_view: Callable[[], Any] = lambda: None
        # The pet cache (pet.py sets it); the room shows the carried pet.
        self.pets: Any = None
        # The marriage records (marriage.py sets it); the room pairs couples.
        self.marriage: Any = None
        self.uin = 0
        self.nickname = ""
        self.gender = 0
        self.login_attempt = 0
        self.hall: directory.HallInfo | None = None
        self.connection: game.GameConnection | None = None
        self.profile: player.LoginResponse | None = None
        self.section: directory.Section | None = None
        # Game type the player picked in the lobby (uiSelRoom GameType);
        # rooms are listed, created and quick-joined for this type.
        self.game_type = 0
        # Room names seen in lobby lists; RESPONSE_ENTER_ROOM_OLD has none.
        self.room_names: dict[int, str] = {}
        # Feature modules (lobby, room, ...) register packet handlers and
        # react to entering or leaving a section.
        self.handlers: dict[int, Callable[[game.Packet], None]] = {}
        self.on_entered: list[Callable[[], None]] = []
        self.on_disconnected: list[Callable[[], None]] = []
        self.state = self._load_state()

    @staticmethod
    def _load_state() -> dict[str, Any]:
        try:
            return json.loads(STATE_FILE.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return {}

    def save_state(self, **values: Any) -> None:
        self.state.update(values)
        STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
        STATE_FILE.write_text(json.dumps(self.state, ensure_ascii=False, indent=2), encoding="utf-8")

    def disconnect(self) -> None:
        connection, self.connection = self.connection, None
        self.profile = None
        if connection is not None:
            connection.close()
            for listener in list(self.on_disconnected):
                listener()

    def send(self, command: int, payload: bytes, route: int = game.ROUTE_PLAYER) -> bool:
        if self.connection is None or self.profile is None:
            return False
        try:
            self.connection.send(command, payload, route)
            return True
        except OSError as error:
            log(f"[session] send 0x{command:04X} failed: {error}")
            return False


def build_session_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    unhandled_commands: set[int] = set()

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        if not callable(function):
            log(f"[session] script callback {name} is missing")
            return None
        return call_script(function, *args)

    def notify(message: str) -> None:
        runtime.show_msgbox(message, "", 0, "UI.SysMsgbox", 0)  # 0: no ui_OnMsgBoxResult action

    # Account login ---------------------------------------------------------

    def Login(uin, _flag, password, _extra=""):
        session.login_attempt += 1
        attempt = session.login_attempt
        uin, password = int(uin), str(password)

        def finish(result: accountauth.AuthResult | None, hall: directory.HallInfo | None, error: str) -> None:
            if attempt != session.login_attempt:
                return  # cancelled by LoginTimeout or superseded
            if result is None or not result.accepted or hall is None:
                script("LoginFailed")
                notify(error or (result.reason if result else "") or "登录失败")
                return
            session.uin, session.nickname, session.gender, session.hall = uin, result.nickname, result.gender, hall
            session.save_state(last_uin=uin)
            log(f"[session] authenticated {uin} ({result.nickname}); {sum(len(c.sections) for c in hall.channels)} sections")
            # The scripts keep their own uin (set by the login button), which
            # go2shop and others pass back to the host.
            script("NotifyMyUin", uin)
            script("LoginOK")

        def work() -> None:
            result, hall, error = None, None, ""
            try:
                result = accountauth.authenticate((session.server_host, ACCOUNT_AUTH_PORT), uin, password)
                if result.accepted:
                    hall = directory.parse_hall(game.request_directory((session.server_host, DIRECTORY_PORT), uin))
            except (OSError, ValueError, accountauth.AuthError) as failure:
                log(f"[session] login failed: {failure}")
                error = f"无法连接服务器 {session.server_host}"
            runtime.post(lambda: finish(result, hall, error))

        threading.Thread(target=work, name="login", daemon=True).start()

    def LoginTimeout(_cancelled=0):
        session.login_attempt += 1

    def GetLastUin():
        return int(session.state.get("last_uin", 0))

    def GetLocalUin():
        return session.uin

    # Section selection (uiSelSect) ------------------------------------------
    # The directory lists channels whose sections each belong to a location;
    # the screen calls locations "zones" and asks for the sections of one
    # channel within one zone.

    def zone_sections(channel_index: int, zone_index: int) -> list[directory.Section]:
        hall = session.hall
        if hall is None or not 0 <= channel_index < len(hall.channels) or not 0 <= zone_index < len(hall.locations):
            return []
        location_id = hall.locations[zone_index].id
        return [section for section in hall.channels[channel_index].sections if section.location_id == location_id]

    def fetch_ChannelCnt():
        return len(session.hall.channels) if session.hall else 0

    def fetch_ZoneNames():
        return [location.name for location in session.hall.locations] if session.hall else []

    def fetch_LastZoneIdx():
        return int(session.state.get("last_zone", 0))

    def fetch_LastChannelIdx():
        return int(session.state.get("last_channel", 0))

    def sc_getZoneCnt(_channel_index=0):
        return len(session.hall.locations) if session.hall else 0

    def sc_getZoneName(_channel_index, zone_index):
        names = fetch_ZoneNames()
        return names[zone_index] if 0 <= zone_index < len(names) else ""

    def GetSectionCnt(channel_index, zone_index):
        return len(zone_sections(channel_index, zone_index))

    def GetSectionInfo(channel_index, zone_index, section_index):
        sections = zone_sections(channel_index, zone_index)
        section = sections[section_index] if 0 <= section_index < len(sections) else None
        info = type("SectionInfo", (), {})()
        info.m_szSectionName = section.name if section else ""
        info.m_nCurrentNumOfPlayer = section.current_players if section else 0
        info.m_nMaxNumOfPlayer = section.max_players if section else 1
        return info

    def Rand(limit):
        return random.randrange(limit) if limit > 0 else 0

    # Game connection --------------------------------------------------------

    def on_packet(packet: game.Packet) -> None:
        handler = session.handlers.get(packet.command)
        if handler is not None:
            handler(packet)
        elif packet.command not in unhandled_commands:
            unhandled_commands.add(packet.command)
            log(f"[session] unhandled command 0x{packet.command:04X} ({len(packet.payload)} bytes)")

    def on_closed(connection: game.GameConnection, reason: str) -> None:
        if connection is not session.connection:
            return
        entered = session.profile is not None
        session.disconnect()
        log(f"[session] game connection closed: {reason}")
        notify("与服务器的连接已断开" if entered else "进入分区失败")

    def on_login(packet: game.Packet) -> None:
        response = player.parse_login_response(packet.payload)
        if response.result != 0:
            session.disconnect()
            notify(response.reason.decode("gbk", "replace") or "进入分区失败")
            return
        session.profile = response
        log(f"[session] entered section {session.section.name if session.section else '?'} as player {response.player_id}")
        # Listeners prepare lobby state before the screen's OnInit reads it.
        for listener in session.on_entered:
            listener()
        runtime.goto_screen("selRoom")

    session.handlers[game.LOGIN_COMMAND] = on_login

    def request_LoginSection(uin, channel_index, zone_index, section_index, _current_zone=0):
        sections = zone_sections(channel_index, zone_index)
        server = session.hall.server(sections[section_index].server_id) if 0 <= section_index < len(sections) else None
        if server is None:
            notify("分区不存在")
            return
        section = sections[section_index]
        session.disconnect()
        session.section = section
        session.save_state(last_channel=int(channel_index), last_zone=int(zone_index))
        role_id = int(session.state.get("last_role", DEFAULT_ROLE_ID))
        version = session.hall.version

        def work() -> None:
            try:
                holder: list[game.GameConnection] = []
                connection = game.GameConnection(
                    (server.ip, server.port), int(uin), section.id,
                    on_packet=lambda packet: runtime.post(lambda: on_packet(packet)),
                    on_closed=lambda reason: runtime.post(lambda: on_closed(holder[0], reason)),
                )
                holder.append(connection)
                runtime.post(lambda: setattr(session, "connection", connection))
                connection.send(game.LOGIN_COMMAND, player.build_login_request(int(uin), session.nickname, session.gender, section.id, role_id, version))
            except OSError as error:
                log(f"[session] section login failed: {error}")
                runtime.post(lambda: notify(f"无法连接分区服务器 {server.ip}:{server.port}"))

        threading.Thread(target=work, name="section-login", daemon=True).start()

    def LogoutSection(*_args):
        session.disconnect()

    excluded = ("script", "notify", "zone_sections", "on_packet", "on_closed", "on_login")
    return {name: value for name, value in locals().items() if callable(value) and name not in excluded and not name.startswith("_")}
