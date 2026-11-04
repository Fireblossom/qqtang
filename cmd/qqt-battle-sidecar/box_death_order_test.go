package main

import (
	"bytes"
	"strings"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func TestBoxFinalHarmAndDeathAreIdempotentInEitherOrder(t *testing.T) {
	for _, deathFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "harm_first", true: "death_first"}[deathFirst], func(t *testing.T) {
			match := boxTestMatch(t, 2)
			match.local = 3 // observe the arbitrator's independent sender stream
			var log bytes.Buffer
			match.log = &log
			position := match.engine.Actors()[1].Position
			for hit := uint32(0); hit < 3; hit++ {
				match.receiveBoxHarm(game.EntityHarmedEvent{ObjectID: 2, Time: 1000 + hit*3000, PosX: uint16(position.X), PosY: uint16(position.Y)}, game.NotifyPlayerKnocked)
			}
			harm := game.EntityHarmedEvent{ObjectID: 2, Time: 10000, PosX: uint16(position.X), PosY: uint16(position.Y)}
			body, err := (game.PlayerDeathEvent{PlayerID: 2, ClientTime: 10000, PosX: uint16(position.X), PosY: uint16(position.Y)}).MarshalNetworkBinary()
			if err != nil {
				t.Fatal(err)
			}
			eliminations := 0
			collect := func(events []battleengine.Event) {
				for _, event := range events {
					if event.Kind == battleengine.EventActorEliminated {
						eliminations++
					}
				}
			}
			die := func() {
				collect(match.applyRemote(1, game.BattleMessageData{DataID: game.NotifyPlayerDieEvent, Data: body}))
			}
			if deathFirst {
				die()
				collect(match.receiveBoxHarm(harm, game.NotifyPlayerKnocked))
			} else {
				collect(match.receiveBoxHarm(harm, game.NotifyPlayerKnocked))
				die()
			}
			die()
			die()
			actor := match.engine.Actors()[1]
			if actor.State != battleengine.ActorEliminated || actor.DurabilityLeft != 0 || eliminations != 1 || !match.box.finishing {
				t.Fatalf("actor state%v layers%d eliminations%d finishing%v", actor.State, actor.DurabilityLeft, eliminations, match.box.finishing)
			}
			if strings.Contains(log.String(), "rejected") {
				t.Fatalf("valid final notification rejected: %s", log.String())
			}
		})
	}
}
