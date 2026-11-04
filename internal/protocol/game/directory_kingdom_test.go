package game

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func originalKingdomPacket(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/kingdom_request_v848.hex")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func TestKingdomRequestAndResponseRetainNativeCorrelation(t *testing.T) {
	packet := originalKingdomPacket(t)
	request, err := DecodeLocalKingdomRequest(packet)
	if err != nil {
		t.Fatal(err)
	}
	if request.UIN != 1000007 || request.Time != 0x21955398 {
		t.Fatalf("unexpected original kingdom request: %+v", request)
	}
	payload := []byte{0, 0, 1, 2, 3, 4}
	response, err := BuildLocalKingdomResponse(packet, payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectLocalPacket(response)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := InspectLocalPacket(packet)
	if !bytes.Equal(response[4:50], packet[4:50]) || !bytes.Equal(got.Plaintext[:14], want.Plaintext[:14]) || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("response changed original request correlation: %+v", got)
	}
}

func TestKingdomRequestRejectsLookalikes(t *testing.T) {
	packet := originalKingdomPacket(t)
	for name, mutate := range map[string]func(*localPacket, *[]byte){
		"command":   func(p *localPacket, _ *[]byte) { p.Command = 0x0133 },
		"route":     func(p *localPacket, _ *[]byte) { binary.BigEndian.PutUint16(p.Plaintext[8:10], 2) },
		"marker":    func(p *localPacket, _ *[]byte) { binary.BigEndian.PutUint16(p.Plaintext[10:12], 0) },
		"section":   func(p *localPacket, _ *[]byte) { binary.BigEndian.PutUint16(p.Plaintext[12:14], 1) },
		"other-uin": func(_ *localPacket, payload *[]byte) { binary.BigEndian.PutUint32(*payload, 1000006) },
		"zero-uin":  func(_ *localPacket, payload *[]byte) { binary.BigEndian.PutUint32(*payload, 0) },
		"short":     func(_ *localPacket, payload *[]byte) { *payload = (*payload)[:7] },
		"trailing":  func(_ *localPacket, payload *[]byte) { *payload = append(*payload, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeLocalPacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			payload := append([]byte(nil), decoded.Plaintext[14:]...)
			mutate(&decoded, &payload)
			bad, err := buildLocalMessageFromRequest(packet, decoded, decoded.Command, payload, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeLocalKingdomRequest(bad); err == nil {
				t.Fatal("accepted non-kingdom request")
			}
			if _, err := BuildLocalKingdomResponse(bad, []byte{0, 0}); err == nil {
				t.Fatal("answered non-kingdom request")
			}
		})
	}
}
