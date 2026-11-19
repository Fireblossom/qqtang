"""A replay or an offline match that fails while starting closes its sidecar."""

import pathlib
import sys
import types
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))

from qqtclient.battle import offline, replay  # noqa: E402


class FakeSidecar:
    instances: list["FakeSidecar"] = []

    def __init__(self, *args, **kwargs):
        self.closed = False
        FakeSidecar.instances.append(self)

    def start_replay(self, *args):
        return {}  # no "begin": the scene cannot be built

    def start_offline(self, *args):
        return {}

    def close(self):
        self.closed = True


class StartupFailureTest(unittest.TestCase):
    def setUp(self):
        FakeSidecar.instances = []
        self.renderer = types.SimpleNamespace(underlay="stale", overlay="stale")
        self.resources = types.SimpleNamespace(root=pathlib.Path("."))

    def test_replay(self):
        with mock.patch.object(replay, "ReplaySidecar", FakeSidecar), mock.patch.object(replay.qbv, "read", lambda path: object()):
            with self.assertRaises(KeyError):
                replay.ReplayBattle(types.SimpleNamespace(), self.renderer, self.resources, pathlib.Path("x.qbv"), lambda: None)
        self.assertTrue(FakeSidecar.instances and all(sidecar.closed for sidecar in FakeSidecar.instances))
        self.assertIsNone(self.renderer.underlay)

    def test_offline(self):
        with mock.patch.object(offline, "Sidecar", FakeSidecar), mock.patch.object(offline, "AvatarCache", lambda resources: None):
            battle = offline.OfflineBattle(types.SimpleNamespace(namespace={}), self.renderer, self.resources, 311,
                                           [{"id": 1, "team": 1, "role": 1}, {"id": 2, "team": 2, "role": 2, "ai": True}])
            with self.assertRaises(KeyError):
                battle.start()
        self.assertTrue(FakeSidecar.instances and all(sidecar.closed for sidecar in FakeSidecar.instances))
        self.assertIsNone(battle.sidecar)
        self.assertIsNone(self.renderer.underlay)


if __name__ == "__main__":
    unittest.main()
