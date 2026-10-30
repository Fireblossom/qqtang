"""Run the native macOS client: python -m qqtclient --client-root <dir>."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys

PACKAGE_ROOT = pathlib.Path(__file__).resolve().parent.parent


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="qqtclient", description=__doc__)
    parser.add_argument("--client-root", required=True, type=pathlib.Path, help="original client directory (contains Client.exe)")
    parser.add_argument("--scripts", type=pathlib.Path, default=PACKAGE_ROOT / "generated" / "scripts", help="output of tools/prepare-ui.sh")
    parser.add_argument("--server", default="127.0.0.1", help="QQTang server address")
    parser.add_argument("--screen", default="logo", help="first UI screen (logo, login, selSect, ...)")
    parser.add_argument("--report", type=pathlib.Path, default=PACKAGE_ROOT / "generated" / "missing-natives.json", help="where to write the missing-native report on exit")
    practice = parser.add_argument_group("offline practice battle (needs tools/build-sidecar.sh)")
    practice.add_argument("--practice", type=int, metavar="MAP_ID", help="start a practice battle on this map (ordinary rule maps)")
    practice.add_argument("--opponents", type=int, default=1, help="number of wandering opponents (default 1)")
    practice.add_argument("--role", type=int, default=1, help="your role ID (default 1)")
    automation = parser.add_argument_group("automation (for tests and screenshots)")
    automation.add_argument("--headless", action="store_true", help="render off-screen")
    automation.add_argument("--exit-after", type=int, metavar="MS", help="quit after this many milliseconds")
    automation.add_argument("--screenshot", type=pathlib.Path, help="save the last frame here on exit")
    automation.add_argument("--shot", action="append", default=[], metavar="PATH@MS", help="save the frame drawn at MS milliseconds to PATH")
    automation.add_argument("--click", action="append", default=[], metavar="X,Y@MS", help="left-click at X,Y after MS milliseconds")
    automation.add_argument("--rclick", action="append", default=[], metavar="X,Y@MS", help="right-click at X,Y after MS milliseconds")
    automation.add_argument("--drag", action="append", default=[], metavar="X1,Y1:X2,Y2@MS", help="drag with the left button after MS milliseconds")
    automation.add_argument("--type", action="append", default=[], metavar="TEXT@MS", help="type TEXT after MS milliseconds")
    automation.add_argument("--key", action="append", default=[], metavar="NAME@MS", help="press a key (pygame name, e.g. tab, return) after MS milliseconds")
    automation.add_argument("--hold", action="append", default=[], metavar="NAME@FROM:TO", help="hold a key down from FROM to TO milliseconds")
    automation.add_argument("--autoplay", action="store_true",
                            help="in online matches, let the sidecar's test bot play the local player")
    automation.add_argument("--call", action="append", default=[], metavar="EXPR@MS",
                            help="evaluate a native call such as Login(1000001,0,'123456') after MS milliseconds")
    return parser.parse_args()


def scheduled_events(args: argparse.Namespace) -> list[tuple[int, list]]:
    import pygame

    events: list[tuple[int, list]] = []
    for button, specs in ((1, args.click), (3, args.rclick)):
        for spec in specs:
            position, _, at = spec.partition("@")
            x, y = (int(value) for value in position.split(","))
            events.append((int(at or 0), [
                pygame.event.Event(pygame.MOUSEMOTION, pos=(x, y), rel=(0, 0), buttons=(0, 0, 0)),
                pygame.event.Event(pygame.MOUSEBUTTONDOWN, pos=(x, y), button=button),
                pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(x, y), button=button),
            ]))
    for spec in args.drag:
        span, _, at = spec.partition("@")
        (x1, y1), (x2, y2) = (tuple(int(value) for value in point.split(",")) for point in span.split(":"))
        start = int(at or 0)
        middle = ((x1 + x2) // 2, (y1 + y2) // 2)
        events.append((start, [
            pygame.event.Event(pygame.MOUSEMOTION, pos=(x1, y1), rel=(0, 0), buttons=(0, 0, 0)),
            pygame.event.Event(pygame.MOUSEBUTTONDOWN, pos=(x1, y1), button=1),
        ]))
        events.append((start + 100, [pygame.event.Event(pygame.MOUSEMOTION, pos=middle, rel=(0, 0), buttons=(1, 0, 0))]))
        events.append((start + 200, [
            pygame.event.Event(pygame.MOUSEMOTION, pos=(x2, y2), rel=(0, 0), buttons=(1, 0, 0)),
            pygame.event.Event(pygame.MOUSEBUTTONUP, pos=(x2, y2), button=1),
        ]))
    for spec in args.type:
        text, _, at = spec.rpartition("@")
        events.append((int(at or 0), [pygame.event.Event(pygame.TEXTINPUT, text=text)]))
    for spec in args.key:
        name, _, at = spec.partition("@")
        events.append((int(at or 0), [pygame.event.Event(pygame.KEYDOWN, key=pygame.key.key_code(name), mod=0, unicode="")]))
    for spec in args.hold:
        name, _, span = spec.partition("@")
        start, _, end = span.partition(":")
        key = pygame.key.key_code(name)
        events.append((int(start), [pygame.event.Event(pygame.KEYDOWN, key=key, mod=0, unicode="")]))
        events.append((int(end), [pygame.event.Event(pygame.KEYUP, key=key, mod=0, unicode="")]))
    return sorted(events, key=lambda item: item[0])


def main() -> int:
    args = parse_args()
    if args.headless:
        os.environ["SDL_VIDEODRIVER"] = "dummy"
        os.environ["SDL_AUDIODRIVER"] = "dummy"
    os.environ.setdefault("PYGAME_HIDE_SUPPORT_PROMPT", "1")
    import pygame

    from .avatar import AvatarCache
    from .natives import Audio, build_natives
    from .render import Renderer
    from .resources import Resources
    from .runtime import UIRuntime
    from .script import MissingReport, ScriptLoader, new_namespace
    from .chat import build_chat_natives
    from .props import build_props_natives
    from .friend import build_friend_natives
    from .shop import build_shop_natives
    from .lobby import build_lobby_natives
    from .room import build_room_natives
    from .session import Session, build_session_natives

    client_root = args.client_root.resolve()
    if not (client_root / "Client.exe").is_file():
        print(f"{client_root} does not contain Client.exe", file=sys.stderr)
        return 2

    # The original client ran with its own directory as the working directory;
    # scripts write files such as devConfig.txt relative to it.
    scripts = args.scripts.resolve()
    report_path = args.report.resolve()
    os.chdir(client_root)

    pygame.init()
    screen = pygame.display.set_mode((800, 600))
    pygame.display.set_caption("QQ堂")
    resources = Resources(client_root)
    report = MissingReport()
    namespace = new_namespace(report)
    runtime = UIRuntime(ScriptLoader(scripts, namespace))
    namespace.update(build_natives(runtime, resources, Audio(resources)))
    session = Session(args.server)
    session.autoplay = args.autoplay
    namespace.update(build_session_natives(runtime, session))
    namespace.update(build_lobby_natives(runtime, session))
    namespace.update(build_chat_natives(runtime, session))
    namespace.update(build_props_natives(runtime, session))
    namespace.update(build_friend_natives(runtime, session))  # wraps the lobby's player-list natives
    renderer = Renderer(screen, resources)

    def start_battle(begin: bytes, context) -> None:
        from .battle.online import OnlineBattle

        if runtime.activity is not None:
            runtime.activity.close()
            runtime.activity = None
        runtime.activity = OnlineBattle(runtime, renderer, resources, session, begin, context)

    room_natives, draw_room_players = build_room_natives(runtime, session, AvatarCache(resources), start_battle)
    namespace.update(room_natives)
    shop_natives, draw_shop_preview = build_shop_natives(runtime, session, resources, AvatarCache(resources))
    namespace.update(shop_natives)
    renderer.draw_callbacks["SC_shopDemo_draw"] = draw_shop_preview
    renderer.draw_callbacks["SC_room_drawPlayer"] = draw_room_players
    pending = scheduled_events(args)
    calls = sorted(((int(at), expr) for expr, _, at in (spec.rpartition("@") for spec in args.call)), key=lambda item: item[0])
    shots = sorted(((int(at), path) for path, _, at in (spec.rpartition("@") for spec in args.shot)), key=lambda item: item[0])

    clock = pygame.time.Clock()
    started = pygame.time.get_ticks()
    try:
        if args.practice is not None:
            from .battle.practice import PracticeSession

            runtime.activity = PracticeSession(runtime, renderer, resources, args.practice, args.role, args.opponents)
            runtime.start("game")
            runtime.activity.start()
        else:
            runtime.start(args.screen)
        while not runtime.quit_requested:
            elapsed = clock.tick(60)
            now = pygame.time.get_ticks() - started
            while pending and pending[0][0] <= now:
                for event in pending.pop(0)[1]:
                    pygame.event.post(event)
            while calls and calls[0][0] <= now:
                expr = calls.pop(0)[1]
                print(f"[automation] {expr}", flush=True)
                eval(expr, {}, namespace)  # noqa: S307 - developer automation
            for event in pygame.event.get():
                if runtime.activity is not None:
                    runtime.activity.handle(event)
                runtime.handle(event)
            runtime.tick(elapsed)
            if runtime.activity is not None:
                runtime.activity.tick(elapsed)
                if getattr(runtime.activity, "finished", False):
                    runtime.activity = None
            drag = (runtime.drag_image, runtime.mouse_pos) if runtime.dragging is not None and runtime.drag_image else None
            renderer.draw(runtime.root, runtime.focus, drag)
            pygame.display.flip()
            while shots and shots[0][0] <= now:
                pygame.image.save(screen, shots.pop(0)[1])
            if args.exit_after is not None and now >= args.exit_after:
                break
    finally:
        if runtime.activity is not None:
            runtime.activity.close()
        if args.screenshot:
            pygame.image.save(screen, str(args.screenshot))
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(json.dumps(report.as_dict(), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        pygame.quit()
    return 0


if __name__ == "__main__":
    sys.exit(main())
