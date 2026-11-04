package main

// slotIdentity distinguishes an exhausted/reacquired stack from its old
// position, even if consumption and acquisition land in the same host step.
type slotIdentity struct {
	item       uint32
	generation uint32
}

// 005B974A uses the local room bar, skips holes, and matches only the first
// seven GAME_BEGIN entries. An omitted order preserves the old CLI contract.
func selectPreparedOrder(items []preparedView, order []uint32) []preparedView {
	items = items[:min(len(items), 7)]
	if order == nil {
		return append([]preparedView(nil), items...)
	}
	var selected []preparedView
	for _, id := range order[:min(len(order), 7)] {
		if id <= 20000 || id >= 23000 {
			continue
		}
		for _, item := range items {
			if item.Item == id {
				selected = append(selected, item)
				break
			}
		}
	}
	return selected
}

// InitPlayerProp skips a prop when the original factory has no definition.
// Keep the no-resource test/CLI fallback only when no table was loaded.
func (match *onlineMatch) filterPreparedDefinitions() {
	if len(match.prepared) == 0 {
		return
	}
	match.loadPropTable()
	if match.propTable == nil {
		return
	}
	kept := match.prepared[:0]
	for _, item := range match.prepared {
		if _, exists := match.propTable[item.Item]; exists {
			kept = append(kept, item)
		}
	}
	match.prepared = kept
}

func (match *onlineMatch) initSlotOrder() {
	if actor, ok := actorByID(match.engine.Actors(), match.local); ok {
		for _, stack := range actor.HeldActionInventory() {
			match.slotOrder = append(match.slotOrder, slotIdentity{uint32(stack.ActionID), stack.Generation})
		}
	}
	for _, item := range match.prepared {
		match.slotOrder = append(match.slotOrder, slotIdentity{item: item.Item})
	}
}

// The native 005C9A18 vector appends new stacks, stacks existing IDs in
// place, and 005C9D4C erases exhausted entries. Seven slots are a viewport.
func (match *onlineMatch) mixedSlots() [][2]uint32 {
	if match.slotOrder == nil {
		match.initSlotOrder()
	}
	counts := map[slotIdentity]uint32{}
	var additions []slotIdentity
	for _, item := range match.prepared {
		if item.Count > 0 {
			counts[slotIdentity{item: item.Item}] = uint32(item.Count)
		}
	}
	if actor, ok := actorByID(match.engine.Actors(), match.local); ok {
		for _, stack := range actor.HeldActionInventory() {
			key := slotIdentity{uint32(stack.ActionID), stack.Generation}
			counts[key] = stack.Count
			additions = append(additions, key)
		}
	}
	var slots [][2]uint32
	kept := match.slotOrder[:0]
	for _, key := range match.slotOrder {
		if count := counts[key]; count > 0 {
			kept = append(kept, key)
			slots = append(slots, [2]uint32{key.item, count})
			delete(counts, key)
		}
	}
	for _, key := range additions {
		if count := counts[key]; count > 0 {
			kept = append(kept, key)
			slots = append(slots, [2]uint32{key.item, count})
			delete(counts, key)
		}
	}
	match.slotOrder = kept
	return slots
}
