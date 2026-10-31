package game

import (
	"bytes"
	"reflect"
	"testing"
)

func TestNativeBoxIdentityAndPushWire(t *testing.T) {
	element := NewMapElementWire(14112, 0x12345678, 6, 9)
	if !bytes.Equal(element[:], []byte{0x37, 0x20, 0x12, 0x34, 0x56, 0x78, 6, 9}) {
		t.Fatalf("identity %x", element)
	}
	request := PushMapElementRequest{PlayerID: 2, Direction: 0, Power: 5, Time: 0x01020304, Element: element}
	body, _ := request.MarshalNetworkBinary()
	got, err := ParsePushMapElementRequest(GameEvent{Schema: RequestPushMapElement, Body: body})
	if err != nil || got != request {
		t.Fatalf("request %+v %v", got, err)
	}
	notice := PushMapElementNotify{PlayerID: 2, Direction: 0, Power: 5, DestinationRow: 6, DestinationCol: 13, CurrentX: 380, CurrentY: 260, Time: 0x01020304, Element: element}
	body, _ = notice.MarshalNetworkBinary()
	p, err := ParsePushMapElementNotify(GameEvent{Schema: NotifyPushMapElement, Body: body})
	if err != nil || p != notice {
		t.Fatalf("notice %+v %v", p, err)
	}
}
func TestNativeBoxStopDestroyGenerateRoundTrip(t *testing.T) {
	element := NewMapElementWire(14112, 0x69, 6, 9)
	stop := MapElementStopEvent{Time: 9000, Boxes: []BoxWire{{Row: 6, Col: 9, StopRow: 6, StopCol: 11, MapElementID: 14112, UniqueID: 0x69}}}
	body, _ := stop.MarshalNetworkBinary()
	s, err := ParseMapElementStopEvent(GameEvent{Schema: NotifyMapElementStop, Body: body})
	if err != nil || !reflect.DeepEqual(s, stop) {
		t.Fatalf("stop %+v %v", s, err)
	}
	destroy := MapElementsExplodedEvent{PlayerID: 2, Time: 10000, Elements: []MapElementWire{element}}
	body, _ = destroy.MarshalNetworkBinary()
	d, err := ParseMapElementsExplodedEvent(GameEvent{Schema: NotifyMapElementExploded, Body: body})
	if err != nil || !reflect.DeepEqual(d, destroy) {
		t.Fatalf("destroy %+v %v", d, err)
	}
	generate := GenerateBoxesEvent{Elements: []MapElementWire{element}}
	body, _ = generate.MarshalNetworkBinary()
	g, err := ParseGenerateBoxesEvent(GameEvent{Schema: NotifyGenerateBox, Body: body})
	if err != nil || !reflect.DeepEqual(g, generate) {
		t.Fatalf("generate %+v %v", g, err)
	}
}
