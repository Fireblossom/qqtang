package battleengine

import "fmt"

// RegisterRuleNPCOwner reserves a native rule-8 entity identity without
// adding it to human participants, team outcomes or policy observations.
// Identities remain known after death so already-created bubbles can finish;
// the rule controller owns the active/dead lifecycle.
func (engine *Engine) RegisterRuleNPCOwner(id uint16) error {
	if engine == nil || !engine.rules.NativeBoxPhysics || id < 20001 || id >= 23000 {
		return fmt.Errorf("rule NPC owner %d is not permitted", id)
	}
	if engine.ruleNPCOwners == nil {
		engine.ruleNPCOwners = map[uint16]struct{}{}
	}
	engine.ruleNPCOwners[id] = struct{}{}
	return nil
}

func (engine *Engine) HasRuleNPCOwner(id uint16) bool {
	if engine == nil {
		return false
	}
	_, ok := engine.ruleNPCOwners[id]
	return ok
}

// PlaceRuleNPCBomb is an explicit scene-controller operation. Personal
// capacity and native AI safety checks belong to that controller; these
// bubbles never consume a human's placement allowance.
func (engine *Engine) PlaceRuleNPCBomb(owner uint16, cell Cell, wirePower uint16, property byte, at uint32) (Event, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return Event{}, fmt.Errorf("unregistered rule NPC owner %d", owner)
	}
	if _, inside := engine.grid.Cell(cell); !inside || wirePower < 1 || wirePower > 16 {
		return Event{}, fmt.Errorf("invalid rule NPC bomb at %v with power %d", cell, wirePower)
	}
	bomb := Bomb{ID: engine.nextBombID, OwnerID: owner, Cell: cell, Power: byte(wirePower - 1),
		RuleOwned: true, NativeStyle: 1, ExplodeAtMS: saturatingAdd(at, NativeBombFuseMS+1), SceneFourEffect: property != 0}
	return engine.commitBomb(bomb), nil
}

// CollectRuleNPCPickup removes a concrete visible object for the independent
// rule controller. It does not create a human actor or apply human inventory
// effects; the authenticated rule controller owns those NPC attributes.
func (engine *Engine) CollectRuleNPCPickup(owner uint16, scene uint32, cell Cell) (bool, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return false, fmt.Errorf("unregistered NPC pickup owner %d", owner)
	}
	for i := range engine.pickups {
		p := &engine.pickups[i]
		if p.Cell == cell && p.SceneID == scene && p.State == PickupAvailable {
			p.State = PickupCollected
			return true, nil
		}
	}
	return false, nil
}

// InstallRuleNPCDrops consumes an already selected native death-drop table.
// Only registered scene entities can use this explicit non-participant path.
func (engine *Engine) InstallRuleNPCDrops(owner uint16, drops []Pickup) ([]Event, error) {
	if !engine.HasRuleNPCOwner(owner) {
		return nil, fmt.Errorf("unregistered NPC drop owner %d", owner)
	}
	if err := engine.validateVerifiedDeathDrops(drops); err != nil {
		return nil, err
	}
	return engine.installDeathDrops(owner, drops), nil
}
