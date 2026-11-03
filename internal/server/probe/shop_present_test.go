package probe

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"testing"

	"qqtang/internal/protocol/capture"
	"qqtang/internal/protocol/game"
	"qqtang/internal/server/persistence"
)

func TestOfflineGiftWaitsForPrimaryThenDeliversItemAndNote(t *testing.T) {
	store, err := persistence.OpenPlayerStore(filepath.Join(t.TempDir(), "gifts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, uin := range []uint32{1000001, 1000002} {
		p := game.DefaultPlayerProfile()
		p.Inventory = nil
		p.GameInfo.Money = 2500
		store.Save(t.Context(), uin, p)
	}
	_, err = store.GiftInventoryItem(t.Context(), persistence.GiftPlan{SourceUIN: 1000001, TargetUIN: 1000002, CommodityID: 20050, ProductName: "医疗包", Item: game.NewPermanentItemInfo(20050, 1), Price: 1000, Stackable: true, Note: "happy"})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := capture.Open(filepath.Join(t.TempDir(), "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	server := &Server{playerStore: store, liveSessions: make(map[net.Conn]*connectionSession), logWriter: io.Discard, capture: trace}
	server.deliverGiftNotices(1000002)
	notes, _ := store.PendingGiftNotices(t.Context(), 1000002)
	if len(notes) != 1 {
		t.Fatal("offline note was discarded")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	primary := &connectionSession{connection: left}
	primary.liveUIN.Store(1000002)
	primary.notePacket(testLocalRoutedPacket(t, 0x0107, 4, 0xffff, 0, 1000002))
	server.liveSessions[left] = primary
	received := make(chan [][]byte, 1)
	go func() {
		var packets [][]byte
		for i := 0; i < 2; i++ {
			header := make([]byte, 4)
			if _, err := io.ReadFull(right, header); err != nil {
				received <- nil
				return
			}
			packet := make([]byte, binary.BigEndian.Uint32(header))
			copy(packet, header)
			if _, err := io.ReadFull(right, packet[4:]); err != nil {
				received <- nil
				return
			}
			packets = append(packets, packet)
		}
		received <- packets
	}()
	server.deliverGiftNotices(1000002)
	packets := <-received
	if len(packets) != 2 {
		t.Fatal("missing gift delivery")
	}
	item, _ := game.InspectLocalPacket(packets[0])
	notice, err := game.ParsePlayerItemAddNotification(item.Payload)
	if err != nil || item.Command != game.PlayerItemAddNotifyCommand || notice.SourceUIN != 1000001 || notice.UIN != 1000002 || notice.Items[0].ItemID != 20050 {
		t.Fatalf("bad gift inventory: %+v / %v", notice, err)
	}
	note, _ := game.InspectLocalPacket(packets[1])
	if note.Command != game.AcrossSectionChatNotifyCommand || !bytes.Contains(note.Payload, []byte("happy")) {
		t.Fatal("gift note did not use the private notification protocol")
	}
	notes, _ = store.PendingGiftNotices(t.Context(), 1000002)
	if len(notes) != 0 {
		t.Fatal("successful delivery remained pending")
	}
	server.deliverGiftNotices(1000002) // no duplicate packet/write
}
