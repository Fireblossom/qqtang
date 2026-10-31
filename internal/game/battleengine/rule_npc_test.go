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
