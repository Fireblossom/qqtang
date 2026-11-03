package game

import "testing"

func TestMapElementEventRoundTrip(t *testing.T) {
	want := MapElementEvent{EventID: MapElementDoorTransfer, PlayerID: 2, Time: 12345, SrcRow: 1, SrcCol: 1, DestRow: 11, DestCol: 13}
	body, err := want.MarshalNetworkBinary()
	if err != nil || len(body) != 16 {
		t.Fatalf("%d bytes, %v", len(body), err)
	}
	got, err := ParseMapElementEvent(GameEvent{Schema: NotifyMapElement, Body: body})
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
}
