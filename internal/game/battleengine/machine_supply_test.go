package battleengine

import "testing"

func TestMachineSupplyIsImmediateCollectibleAndGated(t *testing.T) {
	config := testConfig()
	config.Participants[0].MaxBombCapacity = 10
	engine := mustEngine(t, config)
	at := engine.Actors()[0].Position
	if _, err := engine.RevealNativeMachinePickup(1, at.Cell()); err == nil {
		t.Fatal("ordinary engine accepted machine supply")
	}
	config.Rules.NativeMachineSupply = true
	engine = mustEngine(t, config)
	before := engine.Actors()[0].BombCapacity
	if _, err := engine.RevealNativeMachinePickup(1, at.Cell()); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyExistingPickupAt(engine.Actors()[0].PlayerID, 1, at); err != nil {
		t.Fatalf("new supply not collectible in the same frame: %v", err)
	}
	if engine.Actors()[0].BombCapacity != before+1 {
		t.Fatal("machine supply did not grant native capacity")
	}
	if _, err := engine.RevealNativeMachinePickup(81, at.Cell()); err == nil {
		t.Fatal("machine supply accepted sugar")
	}
}
