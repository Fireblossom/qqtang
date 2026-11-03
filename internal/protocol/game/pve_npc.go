package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	// NOTIFY_CREATE_PVENPC: the arbitrator's batch of adventure NPCs for the
	// current battlefield (Client.exe 0x006162F6 sends, 0x005FCFD3 receives).
	NotifyCreatePVENPC  = 0x138C
	pveNPCWireSize      = 72
	pveNPCNickNameBytes = 20
	PVENPCMaxCount      = 64
)

// PVENPC is one 72-byte PVENPC (QQTMsgData.bin field order): every field is
// a DWORD except the 20-byte GB2312 nickname. NPCID is the runtime identity
// (30001..30199), NPCBaseID the battlefield XML's NPCID the server counts.
type PVENPC struct {
	NPCID, NPCBaseID uint32
	IsBoss           bool
	NickName         []byte
	ImageID          uint32
	MoveStrategy     uint32
	Defense          uint32
	HP               uint32
	Speed            uint32
	Harm             uint32
	BattleStrategy   uint32
	BornRow, BornCol uint32
	View             uint32
}

type CreatePVENPCEvent struct {
	NPCs []PVENPC
}

func (data CreatePVENPCEvent) MarshalNetworkBinary() ([]byte, error) {
	if len(data.NPCs) == 0 || len(data.NPCs) > PVENPCMaxCount {
		return nil, fmt.Errorf("NOTIFY_CREATE_PVENPC count %d is outside 1..%d", len(data.NPCs), PVENPCMaxCount)
	}
	body := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(data.NPCs)*pveNPCWireSize), uint32(len(data.NPCs)))
	for _, npc := range data.NPCs {
		if len(npc.NickName) > pveNPCNickNameBytes {
			return nil, fmt.Errorf("PVENPC %d nickname is %d bytes, at most %d", npc.NPCID, len(npc.NickName), pveNPCNickNameBytes)
		}
		boss := uint32(0)
		if npc.IsBoss {
			boss = 1
		}
		body = binary.BigEndian.AppendUint32(body, npc.NPCID)
		body = binary.BigEndian.AppendUint32(body, npc.NPCBaseID)
		body = binary.BigEndian.AppendUint32(body, boss)
		name := make([]byte, pveNPCNickNameBytes)
		copy(name, npc.NickName)
		body = append(body, name...)
		for _, v := range []uint32{npc.ImageID, npc.MoveStrategy, npc.Defense, npc.HP, npc.Speed, npc.Harm, npc.BattleStrategy, npc.BornRow, npc.BornCol, npc.View} {
			body = binary.BigEndian.AppendUint32(body, v)
		}
	}
	return body, nil
}

func ParseCreatePVENPCEvent(event GameEvent) (CreatePVENPCEvent, error) {
	if event.Schema != NotifyCreatePVENPC || len(event.Body) < 4 {
		return CreatePVENPCEvent{}, fmt.Errorf("NOTIFY_CREATE_PVENPC schema/body 0x%04X/%d", event.Schema, len(event.Body))
	}
	count := int(binary.BigEndian.Uint32(event.Body))
	if count == 0 || count > PVENPCMaxCount || len(event.Body) != 4+count*pveNPCWireSize {
		return CreatePVENPCEvent{}, fmt.Errorf("NOTIFY_CREATE_PVENPC count/length %d/%d is invalid", count, len(event.Body))
	}
	data := CreatePVENPCEvent{NPCs: make([]PVENPC, 0, count)}
	for i := 0; i < count; i++ {
		b := event.Body[4+i*pveNPCWireSize : 4+(i+1)*pveNPCWireSize]
		u := func(offset int) uint32 { return binary.BigEndian.Uint32(b[offset : offset+4]) }
		name := b[12 : 12+pveNPCNickNameBytes]
		if end := bytes.IndexByte(name, 0); end >= 0 {
			name = name[:end]
		}
		data.NPCs = append(data.NPCs, PVENPC{NPCID: u(0), NPCBaseID: u(4), IsBoss: u(8) != 0, NickName: append([]byte(nil), name...),
			ImageID: u(32), MoveStrategy: u(36), Defense: u(40), HP: u(44), Speed: u(48), Harm: u(52), BattleStrategy: u(56),
			BornRow: u(60), BornCol: u(64), View: u(68)})
	}
	return data, nil
}
