package game

import (
	"encoding/binary"
	"fmt"
)

const UseEmotionBodySize = 12

// UseEmotionEvent mirrors QQTMsgData.bin's NOTIFY_USE_EMOTION (0x1194).
// The old client emits it through REQUEST_PLAY even though the schema name is
// NOTIFY; the game service then relays the direction-specific game event to
// the other participants in the room.
type UseEmotionEvent struct {
	PlayerID  uint32 // DWORD on the wire; native actor IDs fit uint16
	Time      uint32
	EmotionID uint32
}

func ParseUseEmotionEvent(event GameEvent) (UseEmotionEvent, error) {
	if event.Schema != NotifyUseEmotion {
		return UseEmotionEvent{}, fmt.Errorf("use-emotion schema 0x%04X, want 0x%04X", event.Schema, NotifyUseEmotion)
	}
	if len(event.Body) != UseEmotionBodySize {
		return UseEmotionEvent{}, fmt.Errorf("NOTIFY_USE_EMOTION body length %d, want %d", len(event.Body), UseEmotionBodySize)
	}
	decoded := UseEmotionEvent{
		PlayerID:  binary.BigEndian.Uint32(event.Body[0:4]),
		Time:      binary.BigEndian.Uint32(event.Body[4:8]),
		EmotionID: binary.BigEndian.Uint32(event.Body[8:12]),
	}
	if decoded.PlayerID == 0 || decoded.PlayerID > 0xFFFF {
		return UseEmotionEvent{}, fmt.Errorf("NOTIFY_USE_EMOTION invalid player ID %d", decoded.PlayerID)
	}
	if decoded.EmotionID < 1 || decoded.EmotionID > 6 {
		return UseEmotionEvent{}, fmt.Errorf("NOTIFY_USE_EMOTION invalid emotion ID %d", decoded.EmotionID)
	}
	return decoded, nil
}

func (event UseEmotionEvent) MarshalNetworkBinary() ([]byte, error) {
	if event.PlayerID == 0 || event.PlayerID > 0xFFFF || event.EmotionID < 1 || event.EmotionID > 6 {
		return nil, fmt.Errorf("NOTIFY_USE_EMOTION invalid player/emotion ID %d/%d", event.PlayerID, event.EmotionID)
	}
	body := make([]byte, 0, UseEmotionBodySize)
	body = binary.BigEndian.AppendUint32(body, event.PlayerID)
	body = binary.BigEndian.AppendUint32(body, event.Time)
	body = binary.BigEndian.AppendUint32(body, event.EmotionID)
	return body, nil
}
