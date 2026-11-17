package main

import (
	"math/rand"
	"slices"
	"testing"

	"qqtang/internal/game/battleengine"
)

// adventureUIMatch is a rule-12 field with players 1 (local) and 2, both
// refilling one bubble per resume, and one NPC that hates both players.
func adventureUIMatch(t *testing.T, resume uint32) (*onlineMatch, *advNPC) {
	t.Helper()
	m := bossMatch(t, 1)
	m.boss = nil
	rules := onlineRules(1, 1)
	rules.NativeAdventure, rules.ExternalConclusion = true, true
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 600_000, 3000, 500, 19
	var participants []battleengine.Participant
	for _, a := range m.engine.Actors() {
		p := a.Participant
		p.BombCapacity, p.MaxBombCapacity, p.BombResumeMS = 2, 2, resume
		participants = append(participants, p)
	}
	engine, err := battleengine.New(battleengine.Config{Seed: 1, Grid: m.engine.Grid(), Rules: rules, Participants: participants})
	if err != nil {
		t.Fatal(err)
	}
	m.engine = engine
	m.runtime, _ = battleengine.NewRuntime(engine, nil)
	m.departed = map[uint16]bool{}
	advanceTo(t, m, 3000)
	n := &advNPC{enmity: map[uint16]int{1: 30, 2: 45}, battle: true, hp: 50, maxHP: 100, lastFire: map[int]uint32{}}
	n.id = 30300
	m.adventure = &adventureRule{players: map[uint16]*advPlayer{1: {hp: 100, maxHP: 100}, 2: {hp: 100, maxHP: 100}},
		npcs: map[uint16]*advNPC{n.id: n}, bubbles: map[uint32]bool{}, seen: map[string]bool{}, rng: rand.New(rand.NewSource(1))}
	return m, n
}

func TestAdventureLeaverIsDisposedAndLosesTheNPCsEnmity(t *testing.T) {
	m, n := adventureUIMatch(t, 2000)
	m.depart(2)
	m.depart(2) // a repeated LEAVE_ROOM notification changes nothing
	if view := m.adventureView(); !slices.Equal(view.Left, []uint16{2}) {
		t.Fatalf("left %v, want [2]", view.Left)
	}
	if n.enmity[2] != 0 || n.enmity[1] != 30 {
		t.Fatalf("enmity %v: only the leaver's drops to 0 (0x005BC67A with -9999999)", n.enmity)
	}
	m.depart(1) // the local player never leaves its own scene
	if view := m.adventureView(); !slices.Equal(view.Left, []uint16{2}) {
		t.Fatalf("left %v after the local departure", view.Left)
	}
}

func TestAdventureBubbleRingCountsOnlyTheLocalPlayersOwnBubbles(t *testing.T) {
	m, _ := adventureUIMatch(t, 800)
	step := func(actions ...battleengine.Action) {
		t.Helper()
		if _, err := m.runtime.Step(actions); err != nil {
			t.Fatal(err)
		}
		m.adventureTick(m.engine.ElapsedMS())
	}
	step()
	if view := m.adventureView(); view.Placed != 0 || view.ResumeMS != 800 {
		t.Fatalf("placed %d resume %d before any bubble", view.Placed, view.ResumeMS)
	}
	step(battleengine.Action{PlayerID: 2, PlaceBomb: true})
	if m.adventureView().Placed != 0 {
		t.Fatal("the remote player's bubble restarted the local ring")
	}
	step(battleengine.Action{PlayerID: 1, PlaceBomb: true})
	step() // the same bubble stays counted once
	if placed := m.adventureView().Placed; placed != 1 {
		t.Fatalf("placed %d after one bubble, want 1", placed)
	}
	step(battleengine.Action{PlayerID: 1, Move: battleengine.DirectionUp})
	for m.engine.ElapsedMS() < 3600 {
		step(battleengine.Action{PlayerID: 1, Move: battleengine.DirectionUp})
	}
	step(battleengine.Action{PlayerID: 1, PlaceBomb: true})
	if placed := m.adventureView().Placed; placed != 2 {
		t.Fatalf("placed %d after the second bubble, want 2", placed)
	}
}
