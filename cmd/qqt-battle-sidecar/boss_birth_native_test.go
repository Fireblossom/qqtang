package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func TestBossBirthMatchesNativeActorFrames(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "boss_birth_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	sequences, frames := 0, 0
	for scanner.Scan() {
		var row struct {
			Role       uint16
			Arbitrator bool
			Effect     string
			Dt         uint32
			Samples    [][6]int32
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		birth := newBossBirthState()
		for _, sample := range row.Samples {
			ready := row.Effect != "busy" && (row.Effect != "ready_after_3100" || sample[0] >= 3100)
			birth.frame(row.Dt, row.Role, row.Arbitrator, ready)
			if int32(birth.mode) != sample[1] || int32(birth.walk) != sample[2] || int32(birth.counter) != sample[3] || sample[4] != 2 || birth.greetingTimer != sample[5] {
				t.Fatalf("role%d arb%v effect%s at%d: %+v; native %v", row.Role, row.Arbitrator, row.Effect, sample[0], birth, sample)
			}
			frames++
		}
		sequences++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if sequences != 68 || frames != 15496 {
		t.Fatalf("native sequences%d frames%d", sequences, frames)
	}
}

type birthCountingAI struct{ calls int }

func (ai *birthCountingAI) tick(*onlineMatch, *bossActor, uint32) { ai.calls++ }

func TestBossBirthRunsDuringReadyAndCommandsUsePreviousActorFrame(t *testing.T) {
	m := bossMatch(t, 1)
	m.boss.actors = map[uint16]*bossActor{}
	m.actorFrameTime, m.actorFrameTimeSet = 60, true
	m.createBosses(game.CreateNPCBoss{Bosses: []game.CompetitiveBossInfo{{BossID: 30001, RoleID: 32, TeamID: 7, AIType: 3, HP: 5, Rate: 8, Bubble: 8, Power: 9}}}, 3000)
	b := m.boss.actors[30001]
	ai := &birthCountingAI{}
	b.ai = ai
	for frame := uint32(80); frame <= 3060; frame += 20 {
		// AI dispatch is absent during Ready; even after Ready the previous
		// actor frame must still be mode9 until its old counter reaches 3000.
		m.bossTick(frame, nil)
		m.advanceBossBirthFrame(frame)
	}
	if b.bornAt != 60 || b.birth.mode != 9 || b.birth.counter != 3000 || ai.calls != 0 || b.vulnerable(3060) {
		t.Fatalf("Ready consumed birth incorrectly: born%d state%+v calls%d", b.bornAt, b.birth, ai.calls)
	}
	m.bossTick(3080, nil)
	if ai.calls != 0 {
		t.Fatal("AI ran before the actor-frame transition")
	}
	m.advanceBossBirthFrame(3080)
	if !b.vulnerable(3080) {
		t.Fatal("completed native birth remained immune")
	}
	m.bossTick(3100, nil)
	if ai.calls != 1 {
		t.Fatal("first post-birth command was delayed")
	}
	view := m.bossView().Actors[0]
	if view.Born != 60 || view.BirthAge != 3020 || view.BirthMode != 1 {
		t.Fatalf("independent actor clock missing in view: %+v", view)
	}
}

func TestBossBirthGreetingBeforeReadyDoesNotDispatchAI(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	b.role, b.birth, b.bornAt, b.birthFrameAt = 34, newBossBirthState(), 60, 60
	ai := &birthCountingAI{}
	b.ai = ai
	m.advanceBossBirthFrame(80)
	m.bossTick(battleengine.NativeRoundStartClockMS, nil)
	if b.birth.mode != 1 || ai.calls != 0 || len(mirrored(t, m, game.NotifyNPCTalk)) != 1 {
		t.Fatal("greeting shortcut bypassed Ready or missed the talk")
	}
	m.advanceBossBirthFrame(100)
	if len(mirrored(t, m, game.NotifyNPCTalk)) != 0 {
		t.Fatal("greeting repeated")
	}
}

func TestLateBossBirthRetainsProgressAcrossArbitratorTransfer(t *testing.T) {
	m := bossMatch(t, 2)
	b := m.boss.actors[30001]
	b.birth, b.bornAt, b.birthFrameAt, b.protectedUntil = newBossBirthState(), 5000, 5000, 9000
	m.advanceBossBirthFrame(7000)
	m.arbitrator = 2
	m.advanceBossBirthFrame(7020)
	if b.birth.counter != 2020 || b.birth.mode != 9 || b.birth.greetingTimer != -5001 {
		t.Fatalf("takeover restarted birth: %+v", b.birth)
	}
	m.advanceBossBirthFrame(8000)
	if b.birth.mode != 9 {
		t.Fatal("late birth skipped the old-counter boundary")
	}
	m.advanceBossBirthFrame(8020)
	if b.birth.mode != 1 || b.vulnerable(8020) || !b.vulnerable(9000) {
		t.Fatal("late birth failed to finish or changed the independent shield")
	}
}
