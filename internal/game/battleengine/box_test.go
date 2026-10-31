package battleengine

import "testing"

func TestNativeBoxMutationIsExplicitAndRetiresAtStop(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	ordinary := mustEngine(t, config)
	if err := ordinary.SetNativeBoxCell(Cell{Row: 4, Col: 3}, Tile{}); err == nil {
		t.Fatal("ordinary rules admitted box mutation")
	}
	config.Rules.NativeBoxPhysics = true
	engine := mustEngine(t, config)
	source, target := Cell{Row: 4, Col: 3}, Cell{Row: 4, Col: 8}
	tile := Tile{Kind: CellBreakable, MapElementID: 14112, MapElementOccupied: true, Durability: 1, ElementWidth: 1, ElementHeight: 1}
	if err := engine.SetNativeBoxCell(source, tile); err != nil {
		t.Fatal(err)
	}
	if err := engine.MoveNativeBox(source, target, 14112); err != nil {
		t.Fatal(err)
	}
	if old, _ := engine.Grid().Cell(source); old.MapElementOccupied {
		t.Fatal("old box registration survived stop")
	}
	if moved, _ := engine.Grid().Cell(target); moved.MapElementID != 14112 || moved.ElementAnchor != target {
		t.Fatal("box identity did not follow stop")
	}
	if _, ok, err := engine.DestroyNativeBox(target, 14112, 1000); err != nil || !ok {
		t.Fatalf("destroy %t %v", ok, err)
	}
	engine.elapsedMS = 1499
	engine.expireMapRetirements()
	if got, _ := engine.Grid().Cell(target); !got.MapElementOccupied {
		t.Fatal("box retired early")
	}
	engine.elapsedMS = 1500
	engine.expireMapRetirements()
	if got, _ := engine.Grid().Cell(target); got.MapElementOccupied {
		t.Fatal("box did not retire")
	}
}
func TestBoxSceneOneGrantsBombItemWithoutCapacity(t *testing.T) {
	config := testConfig()
	config.Rules.NativeBoxPhysics = true
	engine := mustEngine(t, config)
	before := engine.Actors()[0]
	if _, err := engine.ApplyVerifiedPickup(1, SceneBombCapacitySmall); err != nil {
		t.Fatal(err)
	}
	after := engine.Actors()[0]
	if after.BombCapacity != before.BombCapacity || after.NativeBombPickupCount() != before.NativeBombPickupCount() || actorHeldActionCount(&after, 49) != 1 {
		t.Fatalf("wrong rule8 scene1 effect: %+v", after)
	}
	if _, err := engine.ApplyVerifiedPickup(1, SceneBombPowerSmall); err != nil {
		t.Fatal(err)
	}
	engine.ConsumeBoxBombPower(1)
	if engine.Actors()[0].BombPower != before.BombPower {
		t.Fatal("item49 did not consume its basic power increment")
	}
}
func TestBoxCornerBombIsHiddenShortFuseAndIndependentOfOwnerLife(t *testing.T) {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	config.Rules.NativeBoxPhysics = true
	config.Rules.ExternalConclusion = true
	engine := mustEngine(t, config)
	engine.actors[0].State = ActorEliminated
	e, err := engine.PlaceBoxLauncherBomb(1, Cell{Row: 0, Col: 0}, 3001)
	if err != nil {
		t.Fatal(err)
	}
	b := engine.Bombs()[engine.bombIndexByID(e.BombID)]
	if !b.Invisible || !b.RuleOwned || b.Power != 7 || b.FlightUntilMS != 0 || b.ExplodeAtMS != 4052 {
		t.Fatalf("launcher bubble %+v", b)
	}
}
