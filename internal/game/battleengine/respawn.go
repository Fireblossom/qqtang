package battleengine

import "fmt"

// RespawnActor applies a native NOTIFY_PLAYER_RELIVE (0x0FBA) to an
// eliminated actor. It requires Rules.ExternalConclusion: rule 1 has no
// respawn and its eliminations stay final.
//
// Client.exe's receiver (0x00603754) moves the actor to the centre of the
// message's cell, records that cell as the actor's spawn (0x005AD5E1) and
// switches its state machine to the relive state. The relive state's entry
// (0x00881FE5) is VM-protected; the starting attributes restored here come
// from Tutorial2.qbv, where player 6 placed power-4 bubbles before a kill and
// power-2 bubbles after its relive. Whether the relive animation keeps the
// actor still or protected is not known; the actor is active at once.
func (engine *Engine) RespawnActor(playerID uint16, cell Cell) (Event, error) {
	if engine == nil {
		return Event{}, fmt.Errorf("battle engine is nil")
	}
	if !engine.rules.ExternalConclusion {
		return Event{}, fmt.Errorf("respawn needs external conclusion rules")
	}
	if engine.outcome.Ended {
		return Event{}, fmt.Errorf("battle has already ended")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return Event{}, fmt.Errorf("respawn player %d is not a participant", playerID)
	}
	actor := &engine.actors[index]
	if actor.State != ActorEliminated {
		return Event{}, fmt.Errorf("respawn player %d has state %d, want eliminated", playerID, actor.State)
	}
	if tile, inside := engine.grid.Cell(cell); !inside || tile.Kind != CellOpen {
		return Event{}, fmt.Errorf("respawn cell %d,%d is not open", cell.Row, cell.Col)
	}
	initial := actor.initial
	initial.Spawn = cell
	position := PositionAtCellCenter(cell)
	// 0x005AD322 keeps the death position as the previous coordinate, so an
	// item lying on the respawn cell is picked up like any cell entry.
	*actor = Actor{
		Participant: initial, initial: initial, Position: position, State: ActorActive, Facing: DirectionDown,
		OxygenValue: NativeOxygenValueInitial, PublicBehavior: actor.PublicBehavior,
		nativePreviousPosition: actor.Position,
	}
	return Event{Kind: EventActorRespawned, TimeMS: engine.elapsedMS, PlayerID: playerID, Cell: cell, Position: position, TeamID: actor.TeamID}, nil
}
