package battleengine

import "testing"

func TestRuleNPCOwnerDoesNotConsumeParticipantSlotOrHumanCapacity(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.NativeBoxPhysics = true
	e := mustEngine(t, cfg)
	if err := e.RegisterRuleNPCOwner(20001); err != nil {
		t.Fatal(err)
	}
	if len(e.Actors()) != 2 {
		t.Fatal("NPC became a participant")
	}
	bomb, err := e.PlaceRuleNPCBomb(20001, Cell{Row: 1, Col: 2}, 3, 0, 100)
	if err != nil || bomb.PlayerID != 20001 || len(e.Bombs()) != 1 {
		t.Fatalf("NPC bomb %+v %v", bomb, err)
	}
	if !e.Clone().HasRuleNPCOwner(20001) {
		t.Fatal("clone lost NPC owner")
	}
	if _, err = e.PlaceRuleNPCBomb(20002, Cell{Row: 1, Col: 3}, 3, 0, 100); err == nil {
		t.Fatal("unregistered NPC placed a bomb")
	}
	if err = e.Reset(cfg); err != nil || e.HasRuleNPCOwner(20001) {
		t.Fatal("NPC registry leaked across reset")
	}
}
func TestRuleNPCPickupsAndDeathDropsAreExplicit(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.NativeBoxPhysics = true
	cfg.Pickups = []Pickup{{SceneID: 2, Cell: Cell{Row: 1, Col: 2}, State: PickupAvailable}}
	e := mustEngine(t, cfg)
	e.RegisterRuleNPCOwner(20001)
	if _, err := e.CollectRuleNPCPickup(20002, 2, Cell{Row: 1, Col: 2}); err == nil {
		t.Fatal("unknown NPC consumed pickup")
	}
	taken, err := e.CollectRuleNPCPickup(20001, 2, Cell{Row: 1, Col: 2})
	if err != nil || !taken {
		t.Fatalf("pickup %t %v", taken, err)
	}
	taken, err = e.CollectRuleNPCPickup(20001, 2, Cell{Row: 1, Col: 2})
	if err != nil || taken {
		t.Fatal("pickup consumed twice")
	}
	drops := []Pickup{{SceneID: 2, Cell: Cell{Row: 1, Col: 2}, State: PickupAvailable}}
	if _, err = e.InstallRuleNPCDrops(20002, drops); err == nil {
		t.Fatal("unknown NPC installed drops")
	}
	if events, err := e.InstallRuleNPCDrops(20001, drops); err != nil || len(events) != 1 {
		t.Fatalf("drops %+v %v", events, err)
	}
	if len(e.Actors()) != 2 {
		t.Fatal("NPC item path altered participant slots")
	}
}

func TestBossNPCOwnerIsSeparateOptIn(t *testing.T) {
	cfg := testConfig()
	e := mustEngine(t, cfg)
	if err := e.RegisterRuleNPCOwner(30001); err == nil {
		t.Fatal("Boss owner registered without NativeBossNPC")
	}
	cfg.Rules.NativeBossNPC = true
	e = mustEngine(t, cfg)
	if err := e.RegisterRuleNPCOwner(20001); err == nil {
		t.Fatal("Boss rule admitted a rule-8 NPC identity")
	}
	if err := e.RegisterRuleNPCOwner(30009); err == nil {
		t.Fatal("Boss rule admitted an identity outside CREATE_NPC_BOSS's 8 entries")
	}
	if err := e.RegisterRuleNPCOwner(30001); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlaceRuleNPCBomb(30001, Cell{Row: 1, Col: 2}, 9, 0, 100); err != nil || len(e.Actors()) != 2 {
		t.Fatalf("Boss bubble %v, actors %d", err, len(e.Actors()))
	}
}

func TestBossRewardScenesBecomeRulePickups(t *testing.T) {
	cfg := testConfig()
	e := mustEngine(t, cfg)
	if err := e.AddBossRewardScene(86); err == nil {
		t.Fatal("reward scene admitted without NativeBossNPC")
	}
	cfg.Rules.NativeBossNPC = true
	e = mustEngine(t, cfg)
	if err := e.RegisterRuleNPCOwner(30001); err != nil {
		t.Fatal(err)
	}
	drops := []Pickup{{SceneID: 86, Cell: Cell{Row: 1, Col: 2}, State: PickupAvailable}}
	if _, err := e.InstallRuleNPCDrops(30001, drops); err == nil {
		t.Fatal("unregistered experience scene installed")
	}
	if err := e.AddBossRewardScene(86); err != nil {
		t.Fatal(err)
	}
	if _, err := e.InstallRuleNPCDrops(30001, drops); err != nil {
		t.Fatal(err)
	}
	if !e.Clone().rules.rulePickup(86) {
		t.Fatal("clone lost the Boss reward scene")
	}
}

func TestBossNPCAdmitsOneCooperativeTeam(t *testing.T) {
	cfg := testConfig()
	for i := range cfg.Participants {
		cfg.Participants[i].TeamID = 1
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("one team accepted without NativeBossNPC")
	}
	cfg.Rules.NativeBossNPC = true
	mustEngine(t, cfg)
}

func TestBossGlueSlowsPlayersOnly(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.NativeBossNPC = true
	e := mustEngine(t, cfg)
	if err := e.InstallRuleNPCFieldObjects(30001, 43, []Cell{{Row: 1, Col: 2}}); err == nil {
		t.Fatal("unregistered Boss placed glue")
	}
	e.RegisterRuleNPCOwner(30001)
	if err := e.InstallRuleNPCFieldObjects(30001, 7, []Cell{{Row: 1, Col: 2}}); err == nil {
		t.Fatal("non-field scene accepted")
	}
	if err := e.InstallRuleNPCFieldObjects(30001, 43, []Cell{{Row: 1, Col: 2}}); err != nil || len(e.FieldObjects()) != 1 || e.FieldObjects()[0].ActionID != 43 {
		t.Fatalf("glue %v %+v", err, e.FieldObjects())
	}
}

func TestRuleNPCOwnsDroppedBatch(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.NativeBossNPC = true
	e := mustEngine(t, cfg)
	bombs := []RuleBomb{{Cell: Cell{Row: 1, Col: 2}, Power: 1, FromCol: 0}}
	if _, err := e.DropRuleBatch(30001, 100, bombs, nil); err == nil {
		t.Fatal("unregistered owner dropped bubbles")
	}
	e.RegisterRuleNPCOwner(30001)
	if events, err := e.DropRuleBatch(30001, 100, bombs, nil); err != nil || len(events) != 1 || e.Bombs()[0].OwnerID != 30001 {
		t.Fatalf("Boss batch %v %+v", err, events)
	}
}
