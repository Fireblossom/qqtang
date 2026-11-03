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

func TestAdventurePropsKeepTheFirstOfEachID(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	props, err := LoadAdventureProps(root)
	if err != nil {
		t.Fatal(err)
	}
	scythe := props[21025]
	if scythe.Tier != 101 || scythe.FireInterval() != 4000 || scythe.PrepareTime != 500 || !scythe.HasMove || scythe.MoveDistance != 10 ||
		scythe.MoveSpeed != 300 || scythe.AffectObject != 4 || scythe.Effects["HP"] != -1500 {
		t.Fatalf("21025 %+v", scythe)
	}
	if burn := props[21006]; !burn.HasTrap || burn.TrapTime != 60000 || burn.Effects["HP"] != -200 || burn.Anims.Trap != "huoyanchongjibo_xianjin.eff" {
		t.Fatalf("21006 %+v", burn)
	}
	if fist := props[21022].Anims; fist.Prepare != "xuli.eff" || fist.PrepareX != 10 || fist.PrepareY != 20 || fist.Move != "qiquan.eff" ||
		fist.Affect != "kouzhadan.eff" || fist.AffectY != 20 || fist.Grids != "dadichandou2.eff" {
		t.Fatalf("21022 anims %+v", fist)
	}
	if red := props[20058]; red.Anims.Grids != "honglihua.eff" || red.AffectTime != 10000 {
		t.Fatalf("20058 %+v", red)
	}
	if cure := props[20020]; cure.InitialInterval != 20000 || cure.Tier != 3 || cure.FireInterval() != 15000 || cure.GameMode != 0 {
		t.Fatalf("native cure cooldown %+v", cure)
	}
	if medical := props[20050]; medical.GameMode != 2 || medical.InitialInterval != 0 || medical.FireInterval() != 5000 {
		t.Fatalf("native medical mode/cooldown %+v", medical)
	}
	if rage := props[21090]; rage.Tier != 5 {
		t.Fatalf("21090 must be the first entry (Tier 5): %+v", rage)
	}
	if _, ok := props[21099]; ok {
		t.Fatal("21099 is not in this client's PropSet")
	}
}
