package battleengine

import "fmt"

// ApplyNativeDurabilityHit applies rule 7's PLAYER_BE_HARMED. Client.exe
// 0x005ACB1E consumes one layer and gives surviving players 3000 ms of harm
// protection at their current position. It does not respawn or reset items.
// Avatar hits use ApplyVerifiedActorHit instead and do not consume a layer.
func (engine *Engine) ApplyNativeDurabilityHit(playerID, sourceID uint16, position Position, at uint32) ([]Event, error) {
	if engine == nil || engine.rules.NativeDurabilityLayers == 0 {
		return nil, fmt.Errorf("native durability is not enabled")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return nil, fmt.Errorf("durability player %d is not a participant", playerID)
	}
	actor := &engine.actors[index]
	if actor.State == ActorEliminated || actor.DurabilityLeft == 0 || actor.HarmProtectionExpiresAt > at {
		return nil, nil
	}
	if err := engine.ApplyVerifiedMovementCheckpoint(playerID, position); err != nil {
		return nil, err
	}
	engine.resetActorOnHit(actor)
	return engine.applyDurabilityHit(index, sourceID, at), nil
}

func (engine *Engine) applyDurabilityHit(index int, sourceID uint16, at uint32) []Event {
	actor := &engine.actors[index]
	if actor.DurabilityLeft == 0 {
		return nil
	}
	actor.DurabilityLeft--
	events := []Event{{Kind: EventActorHarmed, TimeMS: at, PlayerID: sourceID, TargetID: actor.PlayerID, Cell: actor.Position.Cell(), Position: actor.Position}}
	if actor.DurabilityLeft == 0 {
		return append(events, engine.eliminateActor(index, sourceID, EliminationUnknown)...)
	}
	actor.State = ActorActive
	actor.TrappedBy, actor.TrappedByBombID, actor.TrapExpiresAt = 0, 0, 0
	actor.HarmProtectionExpiresAt = saturatingAdd(at, 3000)
	return events
}
