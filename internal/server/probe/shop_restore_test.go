package probe

import (
	"encoding/binary"
	"io"
	"qqtang/internal/protocol/game"
	"reflect"
	"testing"
)

func TestPermanentItemRestoreRespondsWithoutChargingOrGranting(t *testing.T) {
	profile := game.DefaultPlayerProfile()
	profile.GameInfo.Money = 2500
	profile.Inventory = []game.ItemInfo{game.NewPermanentItemInfo(19, 1)}
	session := &connectionSession{UIN: 1000001, Profile: profile}
	server := &Server{logWriter: io.Discard}
	p := make([]byte, 16)
	binary.BigEndian.PutUint32(p, 1000001)
	binary.BigEndian.PutUint32(p[8:], 19)
	binary.BigEndian.PutUint32(p[12:], 848)
	request := testLocalRoutedPacketWithPayload(t, game.ShopRestoreCommand, 2, 0xffff, 1, 1000001, p)
	decoded, err := game.DecodeLocalShopRestoreRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response, status, handled := server.handleShopRestore(session, "test", request, decoded)
	if !handled || len(response) == 0 || status != "qqt_shop_restore_unnecessary" {
		t.Fatalf("permanent restore: %s / %v", status, handled)
	}
	if !reflect.DeepEqual(profile, session.Profile) {
		t.Fatal("restore changed the account")
	}
	inspection, err := game.InspectLocalPacket(response)
	if err != nil || binary.BigEndian.Uint16(inspection.Payload) != 1 {
		t.Fatalf("restore response: %+v / %v", inspection, err)
	}
}
