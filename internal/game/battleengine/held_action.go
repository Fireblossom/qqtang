package battleengine

// HeldActionGrantAmount is the bounded policy inventory consequence of a grant.
// The default mode shares its byte saturation and seven-slot limits.
func HeldActionGrantAmount(slots [NativeBattleActionSlots]HeldActionSlot, actionID, count uint8) uint8 {
	_, gain := heldActionGrantSlot(slots, actionID, count)
	return gain
}

func heldActionGrantSlot(slots [NativeBattleActionSlots]HeldActionSlot, actionID, count uint8) (int, uint8) {
	if actionID == 0 || count == 0 {
		return -1, 0
	}
	empty := -1
	for index, slot := range slots {
		if slot.ActionID == actionID {
			remaining := ^uint8(0) - slot.Count
			if count > remaining {
				return index, remaining
			}
			return index, count
		}
		if empty < 0 && slot.ActionID == 0 {
			empty = index
		}
	}
	if empty >= 0 {
		return empty, count
	}
	return -1, 0
}

func actorHeldActionCount(actor *Actor, actionID uint8) uint8 {
	if actor == nil || actionID == 0 {
		return 0
	}
	if actor.nativeActionInventory {
		for _, stack := range actor.nativeHeldActions {
			if stack.ActionID == actionID {
				return uint8(min(stack.Count, 255))
			}
		}
		return 0
	}
	for _, slot := range actor.HeldActions {
		if slot.ActionID == actionID {
			return slot.Count
		}
	}
	return 0
}

// Native mode appends new stacks and retains items beyond the visible page.
// The default bounded mode preserves the seven-slot policy contract.
func grantHeldAction(actor *Actor, actionID, count uint8) bool {
	if actor == nil || actionID == 0 || count == 0 {
		return false
	}
	if actor.nativeActionInventory {
		for i := range actor.nativeHeldActions {
			if actor.nativeHeldActions[i].ActionID == actionID {
				actor.nativeHeldActions[i].Count += uint32(count)
				actor.projectHeldActions()
				return true
			}
		}
		actor.nativeHeldGeneration++
		actor.nativeHeldActions = append(actor.nativeHeldActions, HeldActionStack{actionID, uint32(count), actor.nativeHeldGeneration})
		actor.projectHeldActions()
		return true
	}
	index, gain := heldActionGrantSlot(actor.HeldActions, actionID, count)
	if index < 0 {
		return false
	}
	actor.HeldActions[index].ActionID = actionID
	actor.HeldActions[index].Count += gain
	return true
}

func consumeHeldAction(actor *Actor, actionID uint8) bool {
	if actor == nil || actionID == 0 {
		return false
	}
	if actor.nativeActionInventory {
		for i := range actor.nativeHeldActions {
			stack := &actor.nativeHeldActions[i]
			if stack.ActionID != actionID || stack.Count == 0 {
				continue
			}
			stack.Count--
			if stack.Count == 0 {
				actor.nativeHeldActions = append(actor.nativeHeldActions[:i], actor.nativeHeldActions[i+1:]...)
			}
			actor.projectHeldActions()
			return true
		}
		return false
	}
	for index := range actor.HeldActions {
		slot := &actor.HeldActions[index]
		if slot.ActionID != actionID || slot.Count == 0 {
			continue
		}
		slot.Count--
		if slot.Count == 0 {
			copy(actor.HeldActions[index:], actor.HeldActions[index+1:])
			actor.HeldActions[len(actor.HeldActions)-1] = HeldActionSlot{}
		}
		return true
	}
	return false
}

func removeHeldAction(actor *Actor, actionID uint8) {
	if actor == nil || actionID == 0 {
		return
	}
	if actor.nativeActionInventory {
		for i, stack := range actor.nativeHeldActions {
			if stack.ActionID == actionID {
				actor.nativeHeldActions = append(actor.nativeHeldActions[:i], actor.nativeHeldActions[i+1:]...)
				actor.projectHeldActions()
				return
			}
		}
		return
	}
	for actorHeldActionCount(actor, actionID) != 0 {
		for index := range actor.HeldActions {
			if actor.HeldActions[index].ActionID != actionID {
				continue
			}
			copy(actor.HeldActions[index:], actor.HeldActions[index+1:])
			actor.HeldActions[len(actor.HeldActions)-1] = HeldActionSlot{}
			break
		}
	}
}

// HeldActionInventory returns a detached, ordered list. The seven-slot
// observation remains a bounded projection for existing policies and models.
func (actor Actor) HeldActionInventory() []HeldActionStack {
	if actor.nativeActionInventory {
		return append([]HeldActionStack(nil), actor.nativeHeldActions...)
	}
	var stacks []HeldActionStack
	for _, slot := range actor.HeldActions {
		if slot.ActionID != 0 && slot.Count != 0 {
			stacks = append(stacks, HeldActionStack{ActionID: slot.ActionID, Count: uint32(slot.Count)})
		}
	}
	return stacks
}

func (actor *Actor) projectHeldActions() {
	actor.HeldActions = [NativeBattleActionSlots]HeldActionSlot{}
	for i, stack := range actor.nativeHeldActions {
		if i == NativeBattleActionSlots {
			break
		}
		actor.HeldActions[i] = HeldActionSlot{ActionID: stack.ActionID, Count: uint8(min(stack.Count, 255))}
	}
}

// GrantHeldAction adds count of a rule-granted action (such as rule 6's
// put-down item 66, 0x0087EC6D) to an actor's inventory, as a pickup would.
// It reports whether the inventory had room.
func (engine *Engine) GrantHeldAction(playerID uint16, actionID, count uint8) bool {
	index := engine.actorIndex(playerID)
	if index < 0 {
		return false
	}
	return grantHeldAction(&engine.actors[index], actionID, count)
}

// ConsumeHeldAction removes one of an actor's held actions (0x0088DE67).
func (engine *Engine) ConsumeHeldAction(playerID uint16, actionID uint8) bool {
	index := engine.actorIndex(playerID)
	if index < 0 {
		return false
	}
	return consumeHeldAction(&engine.actors[index], actionID)
}
