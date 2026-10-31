package main

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func (match *onlineMatch) changeArbitrator(player uint16) {
	if player == match.arbitrator || match.departed[player] {
		return
	}
	if _, known := match.uins[player]; !known {
		match.logf("unknown arbitrator %d ignored", player)
		return
	}
	if err := match.engine.SetNativeArbitrator(match.local, player == match.local); err != nil {
		match.logf("arbitrator change rejected: %v", err)
		return
	}
	match.arbitrator = player
	if match.isArbitrator() {
		actor, active := actorByID(match.engine.Actors(), match.local)
		for key, pending := range match.pickupRequests {
			if active && actor.State == battleengine.ActorActive && actor.Position.Cell() == key.cell && match.pickupAvailable(key) {
				match.arbitratePickup(game.GameEvent{Schema: game.RequestGetItem, Body: pending.body})
			}
			delete(match.pickupRequests, key)
		}
	}
	// Retry local intents against the new authority. Shared rule state remains
	// in place; no replay of old authority notifications is manufactured.
	match.carryRequestSent = 0
	if match.bot != nil {
		match.bot.planned = false
	}
	match.logf("arbitrator is now %d at scene time %d", player, match.engine.ElapsedMS())
}

func (match *onlineMatch) collectLooseDispatches() {
	for _, pickup := range match.engine.TakeLandedLooseObjects() {
		if match.sculpture != nil {
			match.sculpture.loose = append(match.sculpture.loose, looseFragment{cell: pickup.Cell, material: byte(pickup.SceneID - sculptureFirstScene + sculptureFirstMaterial)})
			match.logf("recycled fragment %d landed at %d,%d", pickup.SceneID, pickup.Cell.Row, pickup.Cell.Col)
		}
	}
}

func (rule *kickRule) observeItems(items []game.DispatchBombItem) {
	for _, item := range items {
		for i := range rule.pool {
			if rule.pool[i].scene == item.ItemID && rule.pool[i].left > 0 {
				rule.pool[i].left--
				break
			}
		}
	}
}
