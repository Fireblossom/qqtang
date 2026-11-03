package game

import (
	"encoding/binary"
	"testing"
)

func TestShopRestoreUsesNative16ByteRequestAnd136ByteReply(t *testing.T) {
	p := make([]byte, 16)
	binary.BigEndian.PutUint32(p, 1000001)
	binary.BigEndian.PutUint32(p[4:], 123)
	binary.BigEndian.PutUint32(p[8:], 19)
	binary.BigEndian.PutUint32(p[12:], 848)
	packet := makeLocalPacketForTest(t, append(buildInnerHeaderForTest(ShopRestoreCommand), p...))
	r, err := DecodeLocalShopRestoreRequest(packet)
	if err != nil || r.ItemID != 19 || r.ClientVersion != 848 {
		t.Fatalf("restore request: %+v / %v", r, err)
	}
	b, err := BuildLocalShopRestoreResponse(packet, ShopRestoreResponse{ResultID: 1, ItemID: 19, ResultString: "该道具无需修理"})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectLocalPacket(b)
	if err != nil || inspection.Command != ShopRestoreCommand || len(inspection.Payload) != 136 || binary.BigEndian.Uint32(inspection.Payload[2:]) != 19 {
		t.Fatalf("restore response: %+v / %v", inspection, err)
	}
	for _, bad := range [][]byte{p[:15], append(append([]byte{}, p...), 0)} {
		packet := makeLocalPacketForTest(t, append(buildInnerHeaderForTest(ShopRestoreCommand), bad...))
		if _, err := DecodeLocalShopRestoreRequest(packet); err == nil {
			t.Fatal("accepted an incorrectly sized restore request")
		}
	}
}
