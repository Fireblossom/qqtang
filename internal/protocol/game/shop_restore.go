package game

import (
	"encoding/binary"
	"fmt"
)

const ShopRestoreRequestSchema, ShopRestoreResponseSchema = 0x0430, 0x0818

type ShopRestoreRequest struct {
	UIN, ClientTime, ItemID, ClientVersion uint32
}

func DecodeLocalShopRestoreRequest(packet []byte) (ShopRestoreRequest, error) {
	r, err := decodeLocalPacket(packet)
	if err != nil {
		return ShopRestoreRequest{}, err
	}
	p := r.Plaintext[localInnerHeaderSize:]
	if r.Command != ShopRestoreCommand || len(p) != 16 {
		return ShopRestoreRequest{}, fmt.Errorf("invalid shop-restore command or length")
	}
	out := ShopRestoreRequest{UIN: binary.BigEndian.Uint32(p), ClientTime: binary.BigEndian.Uint32(p[4:]), ItemID: binary.BigEndian.Uint32(p[8:]), ClientVersion: binary.BigEndian.Uint32(p[12:])}
	if out.UIN == 0 || out.UIN != r.EnvelopeUIN || out.ItemID == 0 || out.ItemID > 65535 {
		return ShopRestoreRequest{}, fmt.Errorf("invalid shop-restore account or item")
	}
	return out, nil
}

type ShopRestoreResponse struct {
	ResultID        uint16
	ItemID          uint32
	AvailablePeriod uint16
	ResultString    string
}

func BuildLocalShopRestoreResponse(packet []byte, response ShopRestoreResponse) ([]byte, error) {
	if _, err := DecodeLocalShopRestoreRequest(packet); err != nil {
		return nil, err
	}
	r, err := decodeLocalPacket(packet)
	if err != nil {
		return nil, err
	}
	text, err := encodeLegacyGBKText(response.ResultString, 127)
	if err != nil {
		return nil, err
	}
	p := make([]byte, 136)
	binary.BigEndian.PutUint16(p, response.ResultID)
	binary.BigEndian.PutUint32(p[2:], response.ItemID)
	binary.BigEndian.PutUint16(p[6:], response.AvailablePeriod)
	copy(p[8:], text)
	return buildLocalResponse(packet, r, ShopRestoreCommand, p, nil)
}
