package battleengine

import "testing"

func TestAuthorityTransferKeepsClockBombsAndPendingSupply(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.AllowAuthorityTransfer = true
	cfg.Rules.NativeOutcomeAuthority = true
	cfg.Rules.NativeRequestPlayerID = 1
	e := mustEngine(t, cfg)
	placed, err := e.ApplyVerifiedBombPlacementAt(2, Cell{Row: 1, Col: 3}, 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.elapsedMS = 4000
	e.recycledPickupSceneIDs = []uint32{1, 2}
	e.scheduledPickups = []NativeScheduledPickup{{SceneID: 3, DueMS: 60000}}
	e.lastPickupDispatchMS = 3000
	if err = e.SetNativeArbitrator(1, true); err != nil {
		t.Fatal(err)
	}
	if e.ElapsedMS() != 4000 || len(e.Bombs()) != 1 || e.Bombs()[0].ID != placed.BombID || len(e.recycledPickupSceneIDs) != 2 || len(e.scheduledPickups) != 1 || e.lastPickupDispatchMS != 3000 {
		t.Fatal("authority promotion reset mirrored state")
	}
	events, err := e.Step(nil)
	if err != nil {
		t.Fatal(err)
	}
	exploded := false
	for _, event := range events {
		if event.Kind == EventBombExploded && event.PlayerID == 2 {
			exploded = true
		}
	}
	if !exploded {
		t.Fatalf("new authority failed to explode existing bubble: %+v", events)
	}
	if err = e.SetNativeArbitrator(1, false); err != nil || !e.rules.NativeOutcomeAuthority || e.rules.NativeRequestPlayerID != 1 {
		t.Fatal("demotion failed")
	}
	plain := mustEngine(t, testConfig())
	if plain.SetNativeArbitrator(1, true) == nil {
		t.Fatal("ordinary engine accepted non-opt-in authority transfer")
	}
}
func TestAuthorityMirrorConsumesSupplyAndRecyclesVerifiedDestruction(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.AllowAuthorityTransfer = true
	cfg.Rules.NativeOutcomeAuthority = true
	e := mustEngine(t, cfg)
	e.scheduledPickups = []NativeScheduledPickup{{SceneID: 1, DueMS: 60000}, {SceneID: 1, DueMS: 60000}, {SceneID: 2, DueMS: 90000}}
	if err := e.ApplyVerifiedItemDestruction([]Pickup{{SceneID: 3, Cell: Cell{Row: 1, Col: 1}}}); err != nil {
		t.Fatal(err)
	}
	if len(e.recycledPickupSceneIDs) != 1 {
		t.Fatal("follower lost pending recycled object")
	}
	e.ObserveNativePickupDispatch(60000, []NativeScheduledPickup{{SceneID: 1, DueMS: 60000}})
	if len(e.recycledPickupSceneIDs) != 0 || len(e.scheduledPickups) != 2 || e.lastPickupDispatchMS != 60000 {
		t.Fatal("dispatch did not consume exact shadow supply")
	}
}
func TestLooseObjectRecyclesThroughBirdWithoutBecomingOrdinaryPickup(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.ExternalConclusion = true
	cfg.Rules.LooseObjectSceneIDs = [MaxLooseObjectKinds]uint32{161}
	e := mustEngine(t, cfg)
	if e.RecycleLooseObject(162) == nil {
		t.Fatal("unregistered loose object was accepted")
	}
	if err := e.RecycleLooseObject(161); err != nil {
		t.Fatal(err)
	}
	e.elapsedMS = 30000
	events := e.dispatchRecycledPickups()
	if len(events) != 1 || events[0].SceneID != 161 {
		t.Fatalf("bird events %+v", events)
	}
	e.elapsedMS = 40000
	e.activatePendingPickupDispatches()
	landed := e.TakeLandedLooseObjects()
	if len(landed) != 1 || landed[0].SceneID != 161 {
		t.Fatalf("loose landings %+v", landed)
	}
	if len(e.TakeLandedLooseObjects()) != 0 {
		t.Fatal("landing was delivered twice")
	}
	for _, p := range e.pickups {
		if p.SceneID == 161 {
			t.Fatal("fragment became fire-sensitive pickup")
		}
	}
}

func TestDispatchCapacityBroadcastsAllRetiredDelayedIdentities(t *testing.T) {
	cfg := testConfig()
	cfg.Grid = Grid{Width: 15, Height: 13, Cells: make([]Tile, 195)}
	cfg.Rules.AllowAuthorityTransfer = true
	for i := range cfg.Grid.Cells {
		cfg.Grid.Cells[i].FlamePassable = true
	}
	e := mustEngine(t, cfg)
	e.elapsedMS = 60000
	for i := 0; i < 60; i++ {
		e.recycledPickupSceneIDs = append(e.recycledPickupSceneIDs, 1)
	}
	for i := 0; i < 32; i++ {
		e.scheduledPickups = append(e.scheduledPickups, NativeScheduledPickup{SceneID: 2, DueMS: 60000})
	}
	events := e.dispatchRecycledPickups()
	delayed := 0
	for _, event := range events {
		if event.ScheduledDueMS != 0 {
			delayed++
		}
	}
	if len(events) != 92 || delayed != 32 || len(e.scheduledPickups) != 0 || len(e.pendingPickupDispatches) != 64 {
		t.Fatalf("events=%d delayed=%d remaining=%d", len(events), delayed, len(e.scheduledPickups))
	}
}

func TestPromotionDoesNotWrapFutureDispatchClock(t *testing.T) {
	cfg := testConfig()
	cfg.Rules.AllowAuthorityTransfer = true
	cfg.Rules.NativeOutcomeAuthority = true
	e := mustEngine(t, cfg)
	e.elapsedMS = 30000
	e.ObserveNativePickupDispatch(30020, nil)
	e.recycledPickupSceneIDs = []uint32{1}
	if err := e.SetNativeArbitrator(1, true); err != nil {
		t.Fatal(err)
	}
	if got := e.dispatchRecycledPickups(); len(got) != 0 {
		t.Fatal("sender's future frame wrapped dispatch interval")
	}
	e.elapsedMS = 60020
	if got := e.dispatchRecycledPickups(); len(got) != 1 {
		t.Fatalf("next bird missing: %+v", got)
	}
}
