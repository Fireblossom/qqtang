package battleengine

import "fmt"

// SetNativeArbitrator switches only who produces shared outcomes. Callers must
// authenticate the server's 0x0FB1 first. The scene clock, actors, bubbles,
// retirement timers and pending supply survive the switch (Client 00602B03).
func (engine *Engine) SetNativeArbitrator(local uint16, authority bool) error {
	if engine == nil || !engine.rules.AllowAuthorityTransfer {
		return fmt.Errorf("native authority transfer is not enabled")
	}
	if engine.actorIndex(local) < 0 {
		return fmt.Errorf("local authority player %d is not in scene", local)
	}
	engine.rules.NativeOutcomeAuthority = !authority
	engine.rules.LocalHitPlayerID = 0
	engine.rules.NativeRequestPlayerID = 0
	if authority {
		engine.rules.LocalHitPlayerID = local
	} else {
		engine.rules.NativeRequestPlayerID = local
	}
	engine.nativeContactRequests = nil
	return nil
}

// ObserveNativePickupDispatch retires the mirror's shadow queues only after
// accepting the arbitrator's dispatch. Delayed entries retain their original
// due times on 0x0FAE; recycled items are cleared as in 005E2D0A.
func (engine *Engine) ObserveNativePickupDispatch(clock uint32, delayed []NativeScheduledPickup) {
	if engine == nil || !engine.rules.AllowAuthorityTransfer {
		return
	}
	engine.lastPickupDispatchMS = clock
	engine.recycledPickupSceneIDs = nil
	for _, sent := range delayed {
		for i, item := range engine.scheduledPickups {
			if item == sent {
				last := len(engine.scheduledPickups) - 1
				engine.scheduledPickups[i] = engine.scheduledPickups[last]
				engine.scheduledPickups = engine.scheduledPickups[:last]
				break
			}
		}
	}
}

// RecycleLooseObject is the rule layer's explicit departure/drop boundary.
// Only loose scene IDs registered by that rule can enter the bird queue.
func (engine *Engine) RecycleLooseObject(scene uint32) error {
	if engine == nil || !engine.rules.looseObject(scene) {
		return fmt.Errorf("scene %d is not a rule-owned loose object", scene)
	}
	engine.recycledPickupSceneIDs = append(engine.recycledPickupSceneIDs, scene)
	return nil
}

// TakeLandedLooseObjects hands bird landings back to the rule layer rather
// than putting them into ordinary fire-sensitive pickups.
func (engine *Engine) TakeLandedLooseObjects() []Pickup {
	result := engine.landedLooseObjects
	engine.landedLooseObjects = nil
	return result
}
