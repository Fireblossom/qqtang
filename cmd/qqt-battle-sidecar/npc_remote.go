package main

import "qqtang/internal/game/battleengine"

// 005D1348 stores 005C450F's player-table value in Actor+3B0 even for
// rule1/2/8 and PVE NPCs. Owner physics uses 005C452B's distinct NPC table.
func nativeNPCRemoteScalar(rate byte) float32 {
	rate = min(rate, byte(battleengine.MaxNativeSpeedRate))
	milli, _ := battleengine.NativeSpeedScalarMilli(rate)
	return float32(milli) / 1000
}
