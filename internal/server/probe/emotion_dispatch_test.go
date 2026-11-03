package probe

import (
	"qqtang/internal/protocol/game"
	"testing"
)

func TestEmotionUsesTheSendersScenePlayerID(t *testing.T) {
	server, owner, peer, _ := newSculptureDamageTestServer(t)
	for _, sender := range []*connectionSession{owner, peer} {
		body, err := (game.UseEmotionEvent{PlayerID: uint32(sender.Profile.PlayerID), Time: 5000, EmotionID: 1}).MarshalNetworkBinary()
		if err != nil {
			t.Fatal(err)
		}
		result := sendReliableBossRuleEvent(t, server, sender, 1, game.NotifyUseEmotion, body)
		if !result.handled || len(result.response) == 0 {
			t.Fatalf("native emotion rejected: %+v", result)
		}
	}
	// Even the arbitrator cannot use a cosmetic input as another human.
	for _, pair := range [][2]*connectionSession{{owner, peer}, {peer, owner}} {
		body, _ := (game.UseEmotionEvent{PlayerID: uint32(pair[1].Profile.PlayerID), Time: 6000, EmotionID: 2}).MarshalNetworkBinary()
		result := sendReliableBossRuleEvent(t, server, pair[0], 2, game.NotifyUseEmotion, body)
		if len(result.response) != 0 {
			t.Fatal("accepted impersonated emotion")
		}
	}
}
