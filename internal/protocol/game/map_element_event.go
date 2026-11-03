package game

import (
	"encoding/binary"
	"fmt"
)

// NOTIFY_EVENT_MAP_ELEM (0x1176) event IDs, docs/transdoor-strip-re.md §1.5
// and docs/special-elements-re.md §2.3.
const (
	MapElementDoorEnter    = 5    // a player stepped into a teleporter
	MapElementHaloRequest  = 8    // a non-arbitrator pushes against a halo
	MapElementHaloGrant    = 9    // the arbitrator grants the halo bonus
	MapElementStoneKill    = 10   // a non-arbitrator asks for its explode-stone death
	MapElementDoorShuffle  = 0x0D // the arbitrator's teleporter pairing seed
	MapElementDoorTransfer = 0x0E // a player leaves through the paired door
	MapElementTotemActive  = 0x10 // the arbitrator activates a totem
	mapElementEventSize    = 16
)

// MapElementEvent is the 16-byte 0x1176 body (QQTMsgData.bin record at
// 0x47BA4): EventID, PlayerID, Time, SrcRow, SrcCol, DestRow, DestCol.
type MapElementEvent struct {
	EventID          uint16
	PlayerID         uint16
	Time             uint32
	SrcRow, SrcCol   uint16
	DestRow, DestCol uint16
}

func (event MapElementEvent) MarshalNetworkBinary() ([]byte, error) {
	if event.EventID == 0 {
		return nil, fmt.Errorf("map-element event ID must be non-zero")
	}
	body := make([]byte, 0, mapElementEventSize)
	body = binary.BigEndian.AppendUint16(body, event.EventID)
	body = binary.BigEndian.AppendUint16(body, event.PlayerID)
	body = binary.BigEndian.AppendUint32(body, event.Time)
	for _, v := range []uint16{event.SrcRow, event.SrcCol, event.DestRow, event.DestCol} {
		body = binary.BigEndian.AppendUint16(body, v)
	}
	return body, nil
}

func ParseMapElementEvent(event GameEvent) (MapElementEvent, error) {
	if event.Schema != NotifyMapElement {
		return MapElementEvent{}, fmt.Errorf("map-element schema 0x%04X is not 0x1176", event.Schema)
	}
	if len(event.Body) != mapElementEventSize {
		return MapElementEvent{}, fmt.Errorf("map-element body length %d, want %d", len(event.Body), mapElementEventSize)
	}
	b := event.Body
	return MapElementEvent{
		EventID: binary.BigEndian.Uint16(b[0:2]), PlayerID: binary.BigEndian.Uint16(b[2:4]), Time: binary.BigEndian.Uint32(b[4:8]),
		SrcRow: binary.BigEndian.Uint16(b[8:10]), SrcCol: binary.BigEndian.Uint16(b[10:12]),
		DestRow: binary.BigEndian.Uint16(b[12:14]), DestCol: binary.BigEndian.Uint16(b[14:16]),
	}, nil
}
