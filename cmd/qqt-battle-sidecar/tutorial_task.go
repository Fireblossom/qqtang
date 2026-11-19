package main

// Task.txt, the newbie tutorial's script (docs/tutorial-re.md).
//
// Client.exe 0x005C6044 reads it with "int, separator" pairs: every ten
// integers make one record, whatever the line breaks; the loader adds the
// local PlayerID to the first field. A record is 32 bytes in the controller
// (vtable 0x007A2348, object +0x6C): seven shorts, three ints and the state.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"qqtang/internal/game/battleengine"
)

// Task actions (the jump table at 0x005C70D6, and the routine steps of
// 0x005C7185).
const (
	taskWait         = 1  // lock the input, then f7 ms
	taskForcedWalk   = 2  // the local player walks to (f5, f6)
	taskSay          = 3  // the local player speaks hint f9
	taskTeleport     = 5  // the local player jumps to (f5, f6)
	taskSpawn        = 6  // actor f0 appears at (f5, f6)
	taskWalk         = 7  // actor f0 walks to (f5, f6)
	taskActorSay     = 8  // actor f0 speaks hint f9
	taskSwitchMap    = 10 // restart on map f9
	taskBomb         = 11 // actor f0 drops a bubble
	taskActorSayMark = 12 // actor f0 speaks 5 s, then the arrow at (f5, f6)
	taskReach        = 13 // free input until the local player reaches (f5, f6)
	taskTrapped      = 15 // a teammate trapped in a bubble at (f5, f6)
	taskTrapAgain    = 16 // trap actor f0 once more
	taskWaitStep     = 0x17
	taskPause        = 0x18
	taskRoutines     = 25 // read f9 routine groups, then routine mode
	taskTeleportStep = 0x1A
	taskScroll       = 27 // the story scroll
	taskCard         = 28 // the stage's task card
	taskKubiSay      = 29 // Kubi speaks hint f9
	taskKubiSayMark  = 30 // Kubi speaks 5 s, then the arrow at (f5, f6)
	taskLeaveRoutine = 0x1F
	taskKubi         = 32 // Kubi's picture at (f3, f4)
	taskEatItems     = 33 // free input until nine kinds of items are eaten
)

// taskRecord is one Task.txt entry. Cells are (column, row) pairs.
type taskRecord struct {
	Role   int16 // f0: actor, relative to the local player
	Seq    int16 // f1
	Action int16 // f2
	X1, Y1 int16 // f3, f4
	X2, Y2 int16 // f5, f6
	MS     uint32
	Extra  int32 // f8
	Param  int32 // f9: hint, map or group count
}

func (record taskRecord) from() battleengine.Cell {
	return battleengine.Cell{Row: record.Y1, Col: record.X1}
}

func (record taskRecord) to() battleengine.Cell {
	return battleengine.Cell{Row: record.Y2, Col: record.X2}
}

var taskInteger = regexp.MustCompile(`-?[0-9]+`)

// parseTasks reads Task.txt as 0x005C6044 does: integers in tens.
func parseTasks(data []byte) ([]taskRecord, error) {
	numbers := taskInteger.FindAll(data, -1)
	records := make([]taskRecord, 0, len(numbers)/10)
	for start := 0; start+10 <= len(numbers); start += 10 {
		var fields [10]int64
		for index := range fields {
			value, err := strconv.ParseInt(string(numbers[start+index]), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("Task.txt record %d: %w", len(records), err)
			}
			fields[index] = value
		}
		records = append(records, taskRecord{
			Role: int16(fields[0]), Seq: int16(fields[1]), Action: int16(fields[2]),
			X1: int16(fields[3]), Y1: int16(fields[4]), X2: int16(fields[5]), Y2: int16(fields[6]),
			MS: uint32(fields[7]), Extra: int32(fields[8]), Param: int32(fields[9]),
		})
	}
	return records, nil
}

func loadTasks(clientRoot string) ([]taskRecord, error) {
	data, err := os.ReadFile(filepath.Join(clientRoot, "Task.txt"))
	if err != nil {
		return nil, err
	}
	return parseTasks(data)
}
