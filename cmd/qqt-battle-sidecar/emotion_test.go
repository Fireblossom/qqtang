package main

import (
	"testing"

	"qqtang/internal/protocol/game"
)

func TestEmotionPublishesImmediatelyAndHonorsTwoSecondCooldown(t *testing.T) {
	m := testMatch(t)
	// The native component starts with last-send time zero, so the first
	// emotion is available only after the initial two-second interval.
	for m.engine.ElapsedMS() < 2000 {
		m.runtime.Step(nil)
	}
	m.requestEmotion(1)
	if len(m.outbound) != 1 || len(m.tcpOut) != 1 || len(m.emotionView()) != 1 {
		t.Fatal("emotion was not applied and published")
	}
	m.requestEmotion(2)
	if len(m.outbound) != 1 {
		t.Fatal("cooldown allowed a second emotion")
	}
	until := m.engine.ElapsedMS() + 2000
	for m.engine.ElapsedMS() < until {
		if _, err := m.runtime.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	m.requestEmotion(2)
	if len(m.outbound) != 2 || m.emotionView()[0].ID != 2 {
		t.Fatal("emotion was not allowed at the cooldown boundary")
	}
}

func TestEmotionDeduplicatesMirrorsAndExpires(t *testing.T) {
	m := testMatch(t)
	notice := game.UseEmotionEvent{PlayerID: 2, Time: 4000, EmotionID: 6}
	body, _ := notice.MarshalNetworkBinary()
	m.applyRemote(1, game.BattleMessageData{DataID: game.NotifyUseEmotion, Data: body})
	if len(m.emotionView()) != 0 {
		t.Fatal("accepted another sender's emotion")
	}
	m.applyRemote(2, game.BattleMessageData{DataID: game.NotifyUseEmotion, Data: body})
	first := m.emotionView()[0]
	for i := 0; i < 50; i++ {
		m.runtime.Step(nil)
	}
	m.applyEmotion(notice)
	if m.emotionView()[0].Since != first.Since {
		t.Fatal("duplicate mirror restarted the emotion")
	}
	for m.engine.ElapsedMS() <= first.Since+3000 {
		m.runtime.Step(nil)
	}
	if len(m.emotionView()) != 0 {
		t.Fatal("expired emotion remained visible")
	}
}
