package game

import (
	"encoding/binary"
	"fmt"
)

// Native map-element identity: uint16 asset ID, uint32 unique ID, row, col.
func NewMapElementWire(element uint16, id uint32, row, col byte) MapElementWire {
	var out MapElementWire
	binary.BigEndian.PutUint16(out[:2], element)
	binary.BigEndian.PutUint32(out[2:6], id)
	out[6], out[7] = row, col
	return out
}
func (element MapElementWire) ElementID() uint16 { return binary.BigEndian.Uint16(element[:2]) }
func (element MapElementWire) UniqueID() uint32  { return binary.BigEndian.Uint32(element[2:6]) }
func (request PushMapElementRequest) MarshalNetworkBinary() ([]byte, error) {
	if request.PlayerID == 0 || request.Direction > 3 {
		return nil, fmt.Errorf("invalid box push actor/direction")
	}
	body := make([]byte, 16)
	binary.BigEndian.PutUint16(body, request.PlayerID)
	body[2], body[3] = request.Direction, request.Power
	binary.BigEndian.PutUint32(body[4:], request.Time)
	copy(body[8:], request.Element[:])
	return body, nil
}
func (notice PushMapElementNotify) MarshalNetworkBinary() ([]byte, error) {
	if notice.Direction > 3 || !validCompetitiveCell(notice.DestinationRow, notice.DestinationCol) {
		return nil, fmt.Errorf("invalid box push destination/direction")
	}
	body := make([]byte, 26)
	binary.BigEndian.PutUint16(body, notice.PlayerID)
	body[2], body[3] = notice.Direction, notice.Power
	body[4], body[5] = notice.DestinationRow, notice.DestinationCol
	binary.BigEndian.PutUint32(body[6:], notice.CurrentX)
	binary.BigEndian.PutUint32(body[10:], notice.CurrentY)
	binary.BigEndian.PutUint32(body[14:], notice.Time)
	copy(body[18:], notice.Element[:])
	return body, nil
}
func (request DestroyBoxRequest) MarshalNetworkBinary() ([]byte, error) {
	if request.PlayerID == 0 {
		return nil, fmt.Errorf("zero destroy-box actor")
	}
	body := make([]byte, 18)
	binary.BigEndian.PutUint16(body, request.PlayerID)
	binary.BigEndian.PutUint32(body[2:], request.Time)
	binary.BigEndian.PutUint16(body[6:], request.PosX)
	binary.BigEndian.PutUint16(body[8:], request.PosY)
	copy(body[10:], request.Element[:])
	return body, nil
}
func (notice MapElementsExplodedEvent) MarshalNetworkBinary() ([]byte, error) {
	if len(notice.Elements) > maximumStoppedBoxes {
		return nil, fmt.Errorf("too many destroyed boxes")
	}
	body := make([]byte, 7, 7+8*len(notice.Elements))
	binary.BigEndian.PutUint16(body, notice.PlayerID)
	binary.BigEndian.PutUint32(body[2:], notice.Time)
	body[6] = byte(len(notice.Elements))
	for _, element := range notice.Elements {
		body = append(body, element[:]...)
	}
	return body, nil
}
func (notice MapElementStopEvent) MarshalNetworkBinary() ([]byte, error) {
	if len(notice.Boxes) > maximumStoppedBoxes {
		return nil, fmt.Errorf("too many stopped boxes")
	}
	body := make([]byte, 5, 5+12*len(notice.Boxes))
	body[0] = byte(len(notice.Boxes))
	binary.BigEndian.PutUint32(body[1:], notice.Time)
	for _, box := range notice.Boxes {
		body = append(body, box.Row, box.Col, box.StopRow, box.StopCol)
		body = binary.BigEndian.AppendUint32(body, box.MapElementID)
		body = binary.BigEndian.AppendUint32(body, box.UniqueID)
	}
	return body, nil
}
func (notice GenerateBoxesEvent) MarshalNetworkBinary() ([]byte, error) {
	if len(notice.Elements) > maximumBoxElements {
		return nil, fmt.Errorf("too many generated boxes")
	}
	body := []byte{byte(len(notice.Elements))}
	for _, element := range notice.Elements {
		body = append(body, element[:]...)
	}
	return body, nil
}
func (notice MapElementKnockEvent) MarshalNetworkBinary() ([]byte, error) {
	if len(notice.First) != len(notice.Second) || len(notice.First) > maximumBoxPairs {
		return nil, fmt.Errorf("invalid box collision pairs")
	}
	body := make([]byte, 5)
	binary.BigEndian.PutUint32(body, notice.Time)
	body[4] = byte(len(notice.First))
	for _, e := range notice.First {
		body = append(body, e[:]...)
	}
	for _, e := range notice.Second {
		body = append(body, e[:]...)
	}
	return body, nil
}
func (notice PlayerKnockedEvent) MarshalNetworkBinary() ([]byte, error) {
	if notice.PlayerID == 0 {
		return nil, fmt.Errorf("zero knocked player")
	}
	body := make([]byte, 11)
	binary.BigEndian.PutUint16(body, notice.PlayerID)
	binary.BigEndian.PutUint32(body[2:], notice.Time)
	binary.BigEndian.PutUint16(body[6:], notice.PosX)
	binary.BigEndian.PutUint16(body[8:], notice.PosY)
	if notice.IsAvatar {
		body[10] = 1
	}
	return body, nil
}
