package battleengine

import "testing"

func nativeInventoryEngine(t *testing.T) *Engine {
	config := testConfig()
	config.Rules.NativeActionInventory = true
	config.Rules.ExternalConclusion = true
	return mustEngine(t, config)
}

func TestNativeInventoryRetainsHiddenStacksAndLargeCounts(t *testing.T) {
	engine := nativeInventoryEngine(t)
	ids := []uint8{41, 42, 43, 44, 45, 46, 63, 64, 65}
	for _, id := range ids {
		if !engine.GrantHeldAction(1, id, 1) {
			t.Fatalf("native grant rejected action %d", id)
		}
	}
	actor := engine.Actors()[0]
	if len(actor.HeldActionInventory()) != 9 || actor.HeldActions[6].ActionID != 63 {
		t.Fatalf("native inventory/projection %+v", actor.HeldActionInventory())
	}
	if !canUseHeldAction(&engine.actors[0], 64) {
		t.Fatal("owned action beyond the first page is unusable")
	}
	engine.GrantHeldAction(1, 41, 255)
	actor = engine.Actors()[0]
	// Native 0087EC6D + 005C9A18 retains 1+255 as 256, without a byte cap.
	if actor.HeldActionInventory()[0].Count != 256 || actor.HeldActions[0].Count != 255 {
		t.Fatal("native count was truncated or the policy projection overflowed")
	}
	if !engine.ConsumeHeldAction(1, 42) {
		t.Fatal("could not consume second stack")
	}
	actor = engine.Actors()[0]
	if actor.HeldActions[6].ActionID != 64 || len(actor.HeldActionInventory()) != 8 {
		t.Fatal("consumption did not compact and expose the next stack")
	}
}

func TestNativeInventoryCloneAndActorSnapshotsStayDetached(t *testing.T) {
	engine := nativeInventoryEngine(t)
	engine.GrantHeldAction(1, 64, 2)
	snapshot := engine.Actors()[0]
	clone := engine.Clone()
	clone.ConsumeHeldAction(1, 64)
	if engine.Actors()[0].HeldActionInventory()[0].Count != 2 {
		t.Fatal("clone mutated original native inventory")
	}
	engine.ConsumeHeldAction(1, 64)
	if snapshot.HeldActionInventory()[0].Count != 2 {
		t.Fatal("actor snapshot observed a later mutation")
	}
}

func TestNativeInventoryClearsOnDeathAndSurvivesRespawnMode(t *testing.T) {
	engine := nativeInventoryEngine(t)
	engine.GrantHeldAction(1, 64, 1)
	old := engine.Actors()[0].HeldActionInventory()[0].Generation
	if _, err := engine.ApplyVerifiedElimination(1, 2, []Pickup{}); err != nil {
		t.Fatal(err)
	}
	if len(engine.Actors()[0].HeldActionInventory()) != 0 {
		t.Fatal("elimination retained native held items")
	}
	if _, err := engine.RespawnActor(1, Cell{Row: 1, Col: 1}); err != nil {
		t.Fatal(err)
	}
	for id := uint8(41); id < 50; id++ {
		engine.GrantHeldAction(1, id, 1)
	}
	inventory := engine.Actors()[0].HeldActionInventory()
	if len(inventory) != 9 || inventory[0].Generation <= old {
		t.Fatal("respawn reset native inventory mode or stack identities")
	}
}
