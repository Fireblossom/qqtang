package battleengine

import "fmt"

// RevealNativeMachinePickup is 005FF230: create, initialize, position and
// register directly in the ground-item manager. There is no bird/flight delay.
func (engine *Engine) RevealNativeMachinePickup(scene uint32, cell Cell) (Event, error) {
	if engine == nil || !engine.rules.NativeMachineSupply {
		return Event{}, fmt.Errorf("native machine supply is disabled")
	}
	if _, ok := engine.grid.Cell(cell); !ok || scene < 1 || scene > 3 {
		return Event{}, fmt.Errorf("invalid machine pickup %d at %v", scene, cell)
	}
	engine.installAuthoritativePickup(Pickup{SceneID: scene, Cell: cell, State: PickupAvailable})
	return Event{Kind: EventPickupRevealed, TimeMS: engine.elapsedMS, SceneID: scene, Cell: cell}, nil
}
