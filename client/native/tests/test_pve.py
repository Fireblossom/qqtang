import unittest

from qqtclient import pve


class PVELevelTest(unittest.TestCase):
    def test_levels_follow_the_courage_thresholds(self) -> None:
        if not pve.levels():
            self.skipTest("generated/pve is not prepared")
        self.assertEqual(pve.level_for(0).level, 0)
        self.assertEqual(pve.level_for(572).level, 0)
        self.assertEqual(pve.level_for(573).level, 1)
        self.assertEqual(pve.level_for(1289).hp, 500)


if __name__ == "__main__":
    unittest.main()
