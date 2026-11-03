package game

import (
	"encoding/binary"
	"testing"
)

func shopPresentTestPacket(t *testing.T) []byte {
	t.Helper()
	p := make([]byte, 27+4)
	binary.BigEndian.PutUint32(p, 1000001)
	binary.BigEndian.PutUint32(p[4:], 123)
	binary.BigEndian.PutUint32(p[8:], 20050)
	binary.BigEndian.PutUint16(p[12:], 2)
	binary.BigEndian.PutUint16(p[14:], 3)
	binary.BigEndian.PutUint32(p[16:], 1000002)
	p[20] = 4
	copy(p[21:], []byte{0xc0, 0xf1, 0xce, 0xef}) // 礼物
	binary.BigEndian.PutUint32(p[25:], 848)
	binary.BigEndian.PutUint16(p[29:], 1)
	return makeLocalPacketForTest(t, append(buildInnerHeaderForTest(ShopPresentCommand), p...))
}

func TestShopPresentCompactGBKAndResponseLayout(t *testing.T) {
	packet := shopPresentTestPacket(t)
	r, err := DecodeLocalShopPresentRequest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if r.DestinationUIN != 1000002 || r.AttachInfo != "礼物" || r.ClientVersion != 848 || r.AgreeMixedPayment != 1 {
		t.Fatalf("bad compact request: %+v", r)
	}
	b, err := BuildLocalShopPresentResponse(packet, ShopPresentResponse{ResultID: 0, ResultString: "赠送成功", DealType: 2, PayType: 3, PayMoney: 1000})
	if err != nil {
		t.Fatal(err)
	}
	x, err := InspectLocalPacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if x.Command != ShopPresentCommand || len(x.Payload) != 138 || binary.BigEndian.Uint32(x.Payload[134:]) != 1000 {
		t.Fatalf("bad response: %+v", x)
	}
}
