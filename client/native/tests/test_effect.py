import os
import unittest

os.environ.setdefault("SDL_VIDEODRIVER", "dummy")
import pygame  # noqa: E402

from qqtclient.battle.effect import EffectPlayer, parse_effect  # noqa: E402

DOOR = """(9,
[
  [ (8, ('magic8', 0, 100, 20), (1,), (0,), 1, (0, 0)),
    [
      [0, ((24, 17, 0), (0.5, 0.25), 0, (255, 255, 255), 0.5)],
      [1000, ((24, 17, 0), (1.0, 0.5), 90, (255, 0, 0), 1.0)],
    ]
  ],
],
[1,0,]
)"""


class EffectTest(unittest.TestCase):
    def test_parse_and_play(self) -> None:
        effect = parse_effect(DOOR)
        self.assertEqual((effect.length, effect.mode, effect.tracks[0].blend, effect.tracks[0].image_id), (1000, 1, 1, 8))
        image = pygame.Surface((40, 40), pygame.SRCALPHA)
        image.fill((255, 255, 255, 255))
        asked = []
        player = EffectPlayer(lambda name: DOOR.encode() if name == "effect/door.eff" else None,
                              lambda path: asked.append(path) or [(image, -20, -20)])
        surface = pygame.Surface((200, 200))
        self.assertTrue(player.draw(surface, "door.eff", 1500, 50, 50))  # loops
        self.assertEqual(asked[-1], "object/magic/magic0008.img")
        self.assertNotEqual(surface.get_at((74, 67))[:3], (0, 0, 0))  # (50+24, 50+17) lit
        self.assertFalse(player.draw(surface, "missing.eff", 0, 0, 0))

    def test_one_shot_effects_end(self) -> None:
        player = EffectPlayer(lambda name: DOOR.replace("[1,0,]", "[0,0,]").encode(), lambda path: [(pygame.Surface((4, 4)), 0, 0)])
        surface = pygame.Surface((10, 10))
        self.assertTrue(player.draw(surface, "x.eff", 900, 0, 0))
        self.assertFalse(player.draw(surface, "x.eff", 1001, 0, 0))


if __name__ == "__main__":
    unittest.main()
