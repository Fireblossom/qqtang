package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"qqtang/internal/protocol/game"
)

func TestPresencePendingPreservesGameplayUntilRegistered(t *testing.T) {
	match := testMatchAs(t, 2)
	match.begin.GameID = 42
	body := []byte{1, 2, 3, 4}
	match.queue(game.PlayerMoveSchema, 5000, 5000, body)
	match.request(game.RequestGetItem, 5001, body)
	match.flush()
	if len(match.outbound) != 2 {
		t.Fatalf("pending registration lost opening gameplay: %d messages remain", len(match.outbound))
	}
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	match.conn, err = net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer match.conn.Close()
	endpoint := match.localEndpoint()
	ack, err := (game.LegacyUDPControlPacket{
		Header:   game.LegacyUDPControlHeader{PacketNumber: 1, PlayerID: match.local, UIN: match.localUIN, Type: game.LegacyUDPPresenceType},
		Presence: &endpoint,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	match.handleDatagram(ack)
	match.flush()
	if len(match.outbound) != 0 {
		t.Fatal("registered gameplay was not flushed")
	}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 2048)
	for _, schema := range []uint16{game.PlayerMoveSchema, game.RequestGetItem} {
		n, _, err := server.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := game.DecodeLegacyUDPControlPacket(buffer[:n])
		if err != nil || packet.Multicast == nil {
			t.Fatalf("gameplay packet = %+v, %v", packet, err)
		}
		batch, err := game.DecodeQQTPPPGameplayBatch(packet.Multicast.Data)
		if err != nil || len(batch.Entries) != 1 || len(batch.Entries[0].Package.Messages) != 1 {
			t.Fatalf("gameplay batch = %+v, %v", batch, err)
		}
		message := batch.Entries[0].Package.Messages[0]
		if message.DataID != uint32(schema) || !bytes.Equal(message.Data, body) {
			t.Fatalf("pending message changed: %+v", message)
		}
		if len(packet.Multicast.Targets) != 1 || packet.Multicast.Targets[0].PlayerID != 2 {
			t.Fatalf("wrong gameplay target: %+v", packet.Multicast.Targets)
		}
	}
	// A later idle flush must not replay the opening commands.
	match.flush()
	_ = server.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := server.ReadFromUDP(buffer); err == nil {
		t.Fatal("opening gameplay was sent twice")
	}
}
