package game

import (
	"encoding/binary"
	"fmt"

	"qqtang/internal/protocol/directory"
)

// LocalKingdomRequest is schema041E, emitted by the original NetCenter after
// its hall request: Uin and Time, with no account mutations.
type LocalKingdomRequest struct {
	UIN  uint32
	Time uint32
}

func DecodeLocalKingdomRequest(packet []byte) (LocalKingdomRequest, error) {
	decoded, err := InspectLocalPacket(packet)
	if err != nil {
		return LocalKingdomRequest{}, err
	}
	if decoded.Command != directory.KingdomCommand || decoded.Route != 6 || decoded.Marker != 0xFFFF ||
		(decoded.SectionID != 0x00FE && decoded.SectionID != 0x00FD) {
		return LocalKingdomRequest{}, fmt.Errorf("not a local directory kingdom route")
	}
	if len(decoded.Payload) != 8 {
		return LocalKingdomRequest{}, fmt.Errorf("kingdom request payload length %d, want 8", len(decoded.Payload))
	}
	request := LocalKingdomRequest{UIN: binary.BigEndian.Uint32(decoded.Payload), Time: binary.BigEndian.Uint32(decoded.Payload[4:])}
	if request.UIN == 0 || request.UIN != decoded.EnvelopeUIN {
		return LocalKingdomRequest{}, fmt.Errorf("kingdom request UIN does not match its envelope")
	}
	return request, nil
}

func BuildLocalKingdomResponse(packet, payload []byte) ([]byte, error) {
	if _, err := DecodeLocalKingdomRequest(packet); err != nil {
		return nil, err
	}
	decoded, err := decodeLocalPacket(packet)
	if err != nil {
		return nil, err
	}
	return buildLocalResponse(packet, decoded, directory.KingdomCommand, payload, nil)
}
