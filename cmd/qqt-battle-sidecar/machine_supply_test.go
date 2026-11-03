package main

import (
	"math/rand"
	"slices"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func supplyMatch(t *testing.T, local uint16) *onlineMatch {
	m := machineMatch(t, local)
	m.machine.supplyCells = [2][]battleengine.Cell{
		{{Row: 2, Col: 2}, {Row: 4, Col: 4}},
		{{Row: 2, Col: 10}, {Row: 4, Col: 12}},
	}
	return m
}

func TestMachineSupplyNativeIntervalBoundaries(t *testing.T) {
	// Golden first-production clocks measured by running 00888505 with
	// controlled native actor/candidate vectors, RNG and ground-item manager.
	for _, tc := range []struct {
		left, right, actors int
		first               uint32
	}{
		{2, 2, 1, 4067}, {2, 2, 2, 3870}, {2, 2, 8, 3000},
		{1, 1, 2, 7058}, {4, 4, 3, 1983}, {1, 3, 2, 3870}, {2, 4, 2, 2666},
	} {
		rule := &machineRule{supplyCells: [2][]battleengine.Cell{make([]battleengine.Cell, tc.left), make([]battleengine.Cell, tc.right)}}
		if got := rule.supplyInterval(tc.actors); got != tc.first {
			t.Fatalf("native interval %+v: got %d", tc, got)
		}
	}
	m := supplyMatch(t, 1)
	advanceTo(t, m, 3860)
	if got := m.machineSupplyTick(); len(got) != 0 {
		t.Fatal("production before native boundary")
	}
	advanceTo(t, m, 3880)
	if got := m.machineSupplyTick(); len(got) != 2 {
		t.Fatalf("expected one item per half after boundary, got %v", got)
	}
}

func TestMachineSupplySyncCollectReplenishAndTakeover(t *testing.T) {
	owner, peer := supplyMatch(t, 1), supplyMatch(t, 2)
	advanceTo(t, owner, 4000)
	advanceTo(t, peer, 4000)
	if events := owner.machineSupplyTick(); len(events) != 2 {
		t.Fatalf("initial supply: %v", events)
	}
	packets := append([]outboundMessage(nil), owner.outbound...)
	for _, packet := range packets {
		if events := peer.applyRemote(1, packet.data); len(events) != 1 {
			t.Fatalf("peer supply: %v", events)
		}
		if events := peer.applyRemote(1, packet.data); len(events) != 0 {
			t.Fatal("duplicate supply was installed")
		}
	}
	if !slices.Equal(owner.engine.Pickups(), peer.engine.Pickups()) || peer.machine.supplyLast != owner.machine.supplyLast {
		t.Fatal("ground items or cooldowns diverged")
	}
	// Clear only the left half. Replaying an old packet must not resurrect
	// the collected item, even after a different cell has been replenished.
	var left battleengine.Pickup
	for _, p := range peer.engine.Pickups() {
		if p.Cell.Col < 6 {
			left = p
		}
	}
	position := battleengine.Position{X: int32(left.Cell.Col)*40 + 20, Y: int32(left.Cell.Row)*40 + 20}
	if _, err := peer.runtime.AcceptNativePickup(1, left.SceneID, position); err != nil {
		t.Fatal(err)
	}
	key := pickupKey{scene: left.SceneID, cell: left.Cell}
	peer.collected[key] = 1
	if events := peer.applyRemote(1, packets[0].data); len(events) != 0 {
		t.Fatal("old production resurrected collected pickup")
	}
	peer.arbitrator = 2
	advanceTo(t, peer, 7860)
	if events := peer.machineSupplyTick(); len(events) != 0 {
		t.Fatal("takeover reset the cooldown")
	}
	advanceTo(t, peer, 7880)
	peer.machine.rng = rand.New(rand.NewSource(1))
	events := peer.machineSupplyTick()
	if len(events) != 1 || events[0].Cell.Col >= 6 {
		t.Fatalf("occupied right half should remain paused: %v", events)
	}
	if peer.machine.supplyLast[0] != 7880 || peer.machine.supplyLast[1] != 4000 {
		t.Fatalf("independent half clocks %v", peer.machine.supplyLast)
	}
}

func TestMachineSupplyNativeZeroReturnsFromFrameAndCoolClearsPool(t *testing.T) {
	m := supplyMatch(t, 1)
	advanceTo(t, m, 4000)
	m.machine.rng = rand.New(rand.NewSource(6)) // first native choice is zero
	if got := m.machineSupplyTick(); len(got) != 0 || m.machine.supplyLast != [2]uint32{} {
		t.Fatalf("zero choice continued to right half or advanced clocks: %v", got)
	}
	m.machine.destroyed[0] = true
	m.machine.rng = rand.New(rand.NewSource(1))
	if got := m.machineSupplyTick(); len(got) != 1 || got[0].Cell.Col <= 8 {
		t.Fatalf("Cool did not stop left supply: %v", got)
	}
	if len(m.machine.supplyCells[0]) != 0 || m.machine.supplyInterval(2) != 7058 {
		t.Fatal("destroyed supply pool remained in subsequent interval calculation")
	}
}

func TestMachineSupplyRejectsWrongAuthorityCellAndOldClock(t *testing.T) {
	m := supplyMatch(t, 2)
	body, _ := (game.ProduceItem{RowAndCol: 0x22, ItemID: 1}).MarshalNetworkBinary()
	message := game.BattleMessageData{DataID: game.NotifyProduceItem, Time: 5000, Data: body}
	if got := m.applyRemote(2, message); len(got) != 0 {
		t.Fatal("non-arbitrator authored supply")
	}
	if got := m.applyRemote(1, message); len(got) != 1 {
		t.Fatal("arbitrator supply missing")
	}
	for _, item := range []game.ProduceItem{{RowAndCol: 0x44, ItemID: 1}, {RowAndCol: 0x27, ItemID: 1}, {RowAndCol: 0x22, ItemID: 81}} {
		body, _ := item.MarshalNetworkBinary()
		at := uint32(6000)
		if item.RowAndCol == 0x44 {
			at = 4999
		}
		if got := m.applyRemote(1, game.BattleMessageData{DataID: game.NotifyProduceItem, Time: at, Data: body}); len(got) != 0 {
			t.Fatalf("accepted invalid/stale supply %+v", item)
		}
	}
}

func TestMachineAutoplayReachesSupplyWithFullThrowCharge(t *testing.T) {
	m := supplyMatch(t, 1)
	actor, _ := actorByID(m.engine.Actors(), 1)
	target := actor.Position.Cell()
	target.Col -= 2
	if _, err := m.engine.RevealNativeMachinePickup(2, target); err != nil {
		t.Fatal(err)
	}
	bot := newAutoplayBot(1)
	bot.machine = m.machine
	m.machine.charges[1] = &machineCharge{count: 5}
	for m.engine.ElapsedMS() < 4000 {
		chosen := bot.act(m.engine)
		if _, err := m.engine.Step([]battleengine.Action{chosen}); err != nil {
			t.Fatal(err)
		}
	}
	for _, pickup := range m.engine.Pickups() {
		if pickup.Cell == target && pickup.State == battleengine.PickupAvailable {
			t.Fatal("charged machine bot never reached the supply")
		}
	}
}
