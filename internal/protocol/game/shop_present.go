package game

import (
	"encoding/binary"
	"fmt"
)

const ShopPresentRequestSchema, ShopPresentResponseSchema = 0x040E, 0x07F6

// The string is compact on the wire, followed by the version and mixed-pay
// flag. The 283-byte schema image reserves 256 bytes; its count is one byte.
type ShopPresentRequest struct {
	UIN               uint32
	ClientTime        uint32
	CommodityID       uint32
	DealType          ShopDealType
	PayType           ShopPaymentMethod
	DestinationUIN    uint32
	AttachInfo        string
	ClientVersion     uint32
	AgreeMixedPayment uint16
}

func DecodeLocalShopPresentRequest(packet []byte) (ShopPresentRequest, error) {
	r, err := decodeLocalPacket(packet)
	if err != nil {
		return ShopPresentRequest{}, err
	}
	p := r.Plaintext[localInnerHeaderSize:]
	if r.Command != ShopPresentCommand || len(p) < 27 || len(p) != 27+int(p[20]) {
		return ShopPresentRequest{}, fmt.Errorf("invalid shop-present command or length")
	}
	n := int(p[20])
	text, err := decodeLegacyGBKSlot(p[21 : 21+n])
	if err != nil {
		return ShopPresentRequest{}, err
	}
	out := ShopPresentRequest{UIN: binary.BigEndian.Uint32(p), ClientTime: binary.BigEndian.Uint32(p[4:]), CommodityID: binary.BigEndian.Uint32(p[8:]),
		DealType: ShopDealType(binary.BigEndian.Uint16(p[12:])), PayType: ShopPaymentMethod(binary.BigEndian.Uint16(p[14:])), DestinationUIN: binary.BigEndian.Uint32(p[16:]),
		AttachInfo: text, ClientVersion: binary.BigEndian.Uint32(p[21+n:]), AgreeMixedPayment: binary.BigEndian.Uint16(p[25+n:])}
	if out.UIN == 0 || out.UIN != r.EnvelopeUIN || out.DestinationUIN == 0 || out.CommodityID == 0 {
		return ShopPresentRequest{}, fmt.Errorf("invalid shop-present account or commodity identity")
	}
	return out, nil
}

type ShopPresentResponse struct {
	ResultID     ShopBuyResult
	ResultString string
	DealType     ShopDealType
	PayType      ShopPaymentMethod
	PayMoney     uint32
}

func BuildLocalShopPresentResponse(packet []byte, response ShopPresentResponse) ([]byte, error) {
	if _, err := DecodeLocalShopPresentRequest(packet); err != nil {
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
	p := make([]byte, 138)
	binary.BigEndian.PutUint16(p, uint16(response.ResultID))
	copy(p[2:130], text)
	binary.BigEndian.PutUint16(p[130:], uint16(response.DealType))
	binary.BigEndian.PutUint16(p[132:], uint16(response.PayType))
	binary.BigEndian.PutUint32(p[134:], response.PayMoney)
	return buildLocalResponse(packet, r, ShopPresentCommand, p, nil)
}
