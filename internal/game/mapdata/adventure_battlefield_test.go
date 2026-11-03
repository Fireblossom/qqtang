package mapdata

import (
	"os"
	"testing"
)

func TestEveryAdventureStageBattlefieldLoads(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded := 0
	for id := uint32(1600); id < 1700; id++ {
		entry, ok := catalog.Map(id)
		if !ok {
			continue
		}
		field, err := LoadAdventureBattlefield(root, entry)
		if err != nil {
			t.Errorf("stage %d: %v", id, err)
			continue
		}
		if field.NativeRule != 12 || field.Battlefield.Width < 15 || len(field.SpawnGroupA) == 0 {
			t.Errorf("stage %d: rule %d, %dx%d, %d spawns", id, field.NativeRule, field.Battlefield.Width, field.Battlefield.Height, len(field.SpawnGroupA))
		}
		loaded++
	}
	if loaded != 50 {
		t.Fatalf("loaded %d stages, want 50", loaded)
	}
}

func TestAdventureConfigsDecodeLikeTheOriginalReader(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	levels, err := LoadAdventureLevels(root)
	if err != nil || len(levels) != 31 {
		t.Fatalf("levels %d %v", len(levels), err)
	}
	if level, _ := AdventureLevelFor(levels, 1289); level.Level != 2 || level.HP != 500 || level.BombPow != 300 || level.ReSpeed != 1.5 {
		t.Fatalf("level for 1289: %+v", level)
	}
	stage, err := LoadAdventureStage(root, 1601)
	if err != nil {
		t.Fatal(err)
	}
	if stage.ContinueID != 1 || stage.NextMapIndex != 1602 || stage.LevelRow != 4 || stage.LevelCol != 39 || len(stage.Fields) != 1 || len(stage.NPCs) != 4 {
		t.Fatalf("stage 1601 %+v", stage)
	}
	if npc := stage.NPCs[0]; npc.NPCID != 30033 || npc.HP != 300 || npc.ImageID != 28 || npc.Bounds.EndCol != 39 || npc.NickName == "" {
		t.Fatalf("NPC %+v", npc)
	}
	catalog, err := LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for id := uint32(1601); id <= 1650; id++ {
		// 1647 is listed in mapDesc but has no battlefield file; route 11
		// skips it (1646 -> 1648).
		if _, ok := catalog.Map(id); !ok || id == 1647 {
			continue
		}
		if _, err := LoadAdventureStage(root, id); err != nil {
			t.Errorf("stage %d: %v", id, err)
		}
	}
	six, _ := LoadAdventureStage(root, 1606)
	if len(six.NPCs) == 0 || len(six.NPCs[0].Skills) != 2 || six.NPCs[0].Skills[0]["SkiLLID"] != "21025" {
		t.Fatalf("stage 1606 skills %+v", six.NPCs)
	}
}
