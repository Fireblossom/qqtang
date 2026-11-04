package directory

import (
	"encoding/binary"
	"fmt"
)

const (
	KingdomCommand       uint16 = 0x0131
	KingdomRequestSchema uint32 = 0x041E
	KingdomSchema        uint32 = 0x0806
	KingdomNativeSize           = 15710
)

// BuildLocalKingdomPayload encodes RESPONSE_KINGDOM, not the hall/channel
// list. The local installation has one realm (DirCfg.ini RealmID1), with one
// TCP directory endpoint reserved as server254. This is independent of the
// hall's LocationID and its game/shop server IDs.
//
// KingdomStamp remains zero, matching the current RESPONSE_HALL_INFO catalog.
func BuildLocalKingdomPayload(config LocalHallNativeConfig) ([]byte, error) {
	if err := validateNativeName("kingdom", config.LocationName, 40); err != nil {
		return nil, err
	}
	if !config.ServerIP.Is4() || config.ServerPort == 0 {
		return nil, fmt.Errorf("kingdom directory requires IPv4 and a nonzero TCP port")
	}
	payload := make([]byte, 0, 76)
	payload = binary.BigEndian.AppendUint16(payload, 0) // Result: success.
	payload = binary.BigEndian.AppendUint32(payload, 0) // KingdomStamp.
	payload = binary.BigEndian.AppendUint32(payload, 1) // KingdomNumber.
	name := make([]byte, 40)
	copy(name, config.LocationName)
	payload = append(payload, name...)
	payload = binary.BigEndian.AppendUint32(payload, 1) // KingdomID / RealmID.
	payload = binary.BigEndian.AppendUint16(payload, 0) // Optional realm DNSNameLen.
	payload = binary.BigEndian.AppendUint32(payload, 1) // DirNum.
	payload = binary.BigEndian.AppendUint32(payload, 254)
	payload = binary.BigEndian.AppendUint16(payload, 0) // Optional directory DNSNameLen.
	ip := config.ServerIP.As4()
	// The decoded DWORD is consumed directly by Winsock as an in_addr, as
	// with the game server address in RESPONSE_HALL_INFO.
	payload = binary.BigEndian.AppendUint32(payload, binary.LittleEndian.Uint32(ip[:]))
	payload = binary.BigEndian.AppendUint32(payload, 1) // ServerPortNum.
	payload = binary.BigEndian.AppendUint16(payload, config.ServerPort)
	return payload, nil
}
