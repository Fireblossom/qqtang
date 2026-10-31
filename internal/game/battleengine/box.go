package battleengine

import "fmt"

// SetNativeBoxCell registers a rule-8 object. Dynamic box simulation lives in
// the rule adapter; the grid still owns player/flame traversal at its native
// registered cell until a stop notification moves it.
func (engine *Engine) SetNativeBoxCell(cell Cell, tile Tile) error {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return fmt.Errorf("native box physics is disabled")
	}
	if _, inside := engine.grid.Cell(cell); !inside {
		return fmt.Errorf("box cell %v outside field", cell)
	}
	tile.ElementAnchor = cell
	engine.grid.Cells[engine.gridIndex(cell)] = tile
	return nil
}
func (engine *Engine) MoveNativeBox(source, target Cell, id uint32) error {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return fmt.Errorf("native box physics is disabled")
	}
	tile, ok := engine.grid.Cell(source)
	if !ok || tile.MapElementID != id {
		return fmt.Errorf("box %d absent at %v", id, source)
	}
	if _, ok := engine.grid.Cell(target); !ok {
		return fmt.Errorf("box destination outside field")
	}
	if source == target {
		return nil
	}
	engine.grid.Cells[engine.gridIndex(source)] = Tile{Kind: CellOpen, FlamePassable: true}
	tile.ElementAnchor = target
	engine.grid.Cells[engine.gridIndex(target)] = tile
	engine.movePendingMapRetirement(source, target)
	return nil
}
func (engine *Engine) DestroyNativeBox(cell Cell, id uint32, at uint32) (Event, bool, error) {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return Event{}, false, fmt.Errorf("native box physics is disabled")
	}
	tile, ok := engine.grid.Cell(cell)
	if !ok || tile.MapElementID != id {
		return Event{}, false, nil
	}
	event, started := engine.beginMapRetirement(cell, []Cell{cell}, at, 0, 0)
	return event, started, nil
}
func (engine *Engine) ClearNativeBox(cell Cell, id uint32) {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return
	}
	if tile, ok := engine.grid.Cell(cell); ok && tile.MapElementID == id {
		engine.grid.Cells[engine.gridIndex(cell)] = Tile{Kind: CellOpen, FlamePassable: true}
		delete(engine.pendingMapRetirements, cell)
	}
}

// RevealNativeBoxPickup detaches the hidden object at the box's final pixel
// cell after its 500ms destruction. The adapter retained the original seeded
// item list while the box moved, so it cannot reveal an unrelated old cell.
func (engine *Engine) RevealNativeBoxPickup(scene uint32, cell Cell) (Event, error) {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return Event{}, fmt.Errorf("native box physics is disabled")
	}
	if _, ok := engine.grid.Cell(cell); !ok {
		return Event{}, fmt.Errorf("box pickup outside field")
	}
	if _, _, ok := supportedPickupEffect(scene); !ok && !engine.rules.pickupSupported(scene) {
		return Event{}, fmt.Errorf("unsupported box pickup %d", scene)
	}
	engine.installAuthoritativePickup(Pickup{SceneID: scene, Cell: cell, State: PickupAvailable})
	return Event{Kind: EventPickupRevealed, TimeMS: engine.elapsedMS, SceneID: scene, Cell: cell}, nil
}

// ApplyNativeBoxIce is 005ACBB7: the non-avatar body gets ten seconds of
// slow movement. The six-byte 1166 carries an avatar flag, not a clock.
func (engine *Engine) ApplyNativeBoxIce(playerID uint16, isAvatar bool) (Event, bool) {
	if engine == nil || !engine.rules.NativeBoxPhysics || isAvatar {
		return Event{}, false
	}
	i := engine.actorIndex(playerID)
	if i < 0 || engine.actors[i].State != ActorActive {
		return Event{}, false
	}
	actor := &engine.actors[i]
	if actor.TransformationSceneID != 0 {
		return Event{}, false
	}
	engine.installMovementStatus(actor, MovementStatusSlow)
	return Event{Kind: EventMovementStatusStarted, TimeMS: engine.elapsedMS, PlayerID: playerID, Cell: actor.Position.Cell(), Position: actor.Position, MovementStatus: MovementStatusSlow, EffectExpiresAt: actor.MovementStatusExpiresAt}, true
}

// ConsumeBoxBombPower mirrors item49's 005AD8DD(1): one basic power pickup
// is subtracted in addition to consuming the held item itself.
func (engine *Engine) ConsumeBoxBombPower(playerID uint16) {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return
	}
	i := engine.actorIndex(playerID)
	if i < 0 {
		return
	}
	actor := &engine.actors[i]
	// 005A94B5 ignores a subtraction larger than the basic +138 count.
	if actor.nativeBasicPickupDelta[1] < 1 {
		return
	}
	actor.nativeBasicPickupDelta[1]--
	engine.updateBoxPower(actor)
}

// PlaceBoxLauncherBomb is 005FE8B1's invisible corner bubble. The original
// 3000ms fuse is shortened by 1950ms; it is not a flying 1159 bubble.
func (engine *Engine) PlaceBoxLauncherBomb(owner uint16, at Cell, clock uint32) (Event, error) {
	if engine == nil || !engine.rules.NativeBoxPhysics {
		return Event{}, fmt.Errorf("native box physics is disabled")
	}
	if engine.actorIndex(owner) < 0 {
		return Event{}, fmt.Errorf("unknown launcher owner")
	}
	if _, ok := engine.grid.Cell(at); !ok {
		return Event{}, fmt.Errorf("launcher outside field")
	}
	bomb := Bomb{ID: engine.nextBombID, OwnerID: owner, Cell: at, Power: 7, RuleOwned: true, Invisible: true, NativeStyle: 1, ExplodeAtMS: saturatingAdd(clock, 1051)}
	return engine.commitBomb(bomb), nil
}

func (engine *Engine) updateBoxPower(actor *Actor) {
	power := int(actor.initial.BombPower) + max(0, int(actor.nativeBasicPickupDelta[1])) + 8*max(0, int(actor.boxUpperPower))
	actor.BombPower = byte(min(int(actor.MaxBombPower), power))
}
