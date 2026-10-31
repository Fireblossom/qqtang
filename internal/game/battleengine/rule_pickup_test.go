package battleengine

import "testing"

func TestRulePickupIsOptInAndDoesNotChangeOrdinaryInventory(t *testing.T) {
	config := testConfig()
	config.Pickups = []Pickup{{SceneID: 119, Cell: config.Participants[0].Spawn, State: PickupAvailable}}
	if _, err := New(config); err == nil {
		t.Fatal("ordinary rule accepted caller-owned pickup")
	}
	config.Rules.RulePickupSceneIDs = [8]uint32{119}
	engine := mustEngine(t, config)
	before := engine.Actors()[0]
	event, err := engine.ApplyExistingPickupAt(before.PlayerID, 119, before.Position)
	if err != nil || event.Kind != EventPickupCollected {
		t.Fatalf("pickup %+v %v", event, err)
	}
	after := engine.Actors()[0]
	if before.Participant != after.Participant || before.CarriedObjectives != after.CarriedObjectives || before.HeldActions != after.HeldActions {
		t.Fatal("rule pickup changed ordinary inventory")
	}
	if _, err = engine.ApplyExistingPickupAt(before.PlayerID, 119, before.Position); err == nil {
		t.Fatal("rule pickup collected twice")
	}
}
