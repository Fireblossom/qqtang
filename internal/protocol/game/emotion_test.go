package game

import "testing"

func TestUseEmotionEventUsesPlayerIDIndependentOfEnvelopeUIN(t *testing.T) {
	original := UseEmotionEvent{PlayerID: 2, Time: 123, EmotionID: 6}
	body, err := original.MarshalNetworkBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseUseEmotionEvent(GameEvent{UIN: 1_000_001, Schema: NotifyUseEmotion, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("decoded emotion = %+v, want %+v", decoded, original)
	}
	for _, invalid := range []UseEmotionEvent{{PlayerID: 1000001, EmotionID: 1}, {PlayerID: 2, EmotionID: 7}, {PlayerID: 2}} {
		if _, err := invalid.MarshalNetworkBinary(); err == nil {
			t.Fatalf("accepted invalid emotion: %+v", invalid)
		}
	}
}
