package main

import (
	"strconv"
	"testing"

	"qqtang/internal/game/battleengine"
)

// tutorialRun plays the tutorial with the driver until its result, or
// fails after limitMS of game time on one map.
func tutorialRun(t *testing.T, options tutorialInit, limitMS uint32) (*tutorialMatch, []uint32) {
	t.Helper()
	options.Quiet = !testing.Verbose()
	options.Autoplay = true
	match, err := newTutorialMatch(offlineClientRoot(t), options)
	if err != nil {
		t.Fatal(err)
	}
	maps := []uint32{match.begin.MapID}
	for match.result == nil {
		if match.engine.ElapsedMS() > limitMS {
			record := match.tasks[0]
			for _, actor := range match.engine.Actors() {
				t.Logf("actor %d role %d team %d state %d at %+v", actor.PlayerID, actor.RoleID, actor.TeamID, actor.State, actor.Position.Cell())
			}
			t.Logf("eaten %v, bombs %+v row0 %+v", match.eaten, match.engine.Bombs(), match.engine.Grid().Cells[9:13])
			t.Fatalf("map %d: stuck at %d ms on record %d action %d (state %d, routine %v)", match.begin.MapID, match.engine.ElapsedMS(), record.Seq, record.Action, match.state, match.routineMode)
		}
		if _, err := match.step(command{Cmd: "step", Ticks: 5}); err != nil {
			t.Fatalf("map %d: %v", match.begin.MapID, err)
		}
		if last := maps[len(maps)-1]; last != match.begin.MapID {
			maps = append(maps, match.begin.MapID)
		}
	}
	return match, maps
}

func TestTutorialPlaysEveryStage(t *testing.T) {
	for _, seed := range []uint32{7, 1, 2, 3} {
		t.Run(strconv.Itoa(int(seed)), func(t *testing.T) { tutorialPlaysEveryStage(t, seed) })
	}
}

func tutorialPlaysEveryStage(t *testing.T, seed uint32) {
	match, maps := tutorialRun(t, tutorialInit{Seed: seed}, 230_000)
	want := []uint32{1501, 1502, 1503, 1504, 311}
	if len(maps) != len(want) {
		t.Fatalf("maps %v, want %v (result %d on %d)", maps, want, *match.result, match.begin.MapID)
	}
	for index := range want {
		if maps[index] != want[index] {
			t.Fatalf("maps %v, want %v", maps, want)
		}
	}
	t.Logf("result %d after %d ms on map 311", *match.result, match.engine.ElapsedMS())
}

func TestTutorialParsesTaskFile(t *testing.T) {
	tasks, err := loadTasks(offlineClientRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 71 {
		t.Fatalf("%d records, want 71", len(tasks))
	}
	first, switchMap := tasks[0], tasks[13]
	if first.Action != taskScroll || first.MS != 12000 || switchMap.Action != taskSwitchMap || switchMap.Param != 1502 {
		t.Fatalf("records 0 and 13: %+v %+v", first, switchMap)
	}
	if last := tasks[len(tasks)-1]; last.Action != taskRoutines || last.Param != 0 {
		t.Fatalf("last record %+v", last)
	}
}

func TestTutorialHintsSkipWithTheBubbleKey(t *testing.T) {
	tasks := "0,0,3,0,0,0,0,5000,3,0\r\n0,1,3,0,0,0,0,5000,3,28\r\n0,2,1,0,0,0,0,100000,0,0\r\n"
	match, err := newTutorialMatch(offlineClientRoot(t), tutorialInit{Tasks: tasks, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	step := func(bomb bool) {
		t.Helper()
		if _, err := match.step(command{Ticks: 1, Actions: []action{{ID: 1, Bomb: bomb}}}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 3; index++ {
		step(false)
	}
	if match.hint == nil || match.hint.Text != 0 || match.hint.Avatar != 368 {
		t.Fatalf("hint %+v, want text 0 with misc368", match.hint)
	}
	step(true)
	if match.hint != nil {
		t.Fatalf("hint %+v after the bubble key", match.hint)
	}
	for index := 0; index < 3; index++ {
		step(false)
	}
	if match.hint == nil || match.hint.Text != 28 {
		t.Fatalf("hint %+v, want text 28", match.hint)
	}
	// Locked input: the bubble key placed nothing.
	if bombs := match.engine.Bombs(); len(bombs) != 0 {
		t.Fatalf("bombs %+v during a hint", bombs)
	}
}

func TestTutorialTrappedTeammateIsRescued(t *testing.T) {
	// 1502's records 18..20: Xiaoqian trapped at (12, 2), then free input
	// until the local player gets there.
	tasks := "0,0,5,0,0,11,4,0,0,0\r\n4,18,15,12,2,12,2,0,3,0\r\n4,19,16,12,2,12,2,0,3,0\r\n0,20,13,3,9,12,4,0,0,0\r\n0,21,1,0,0,0,0,100000,0,0\r\n"
	match, err := newTutorialMatch(offlineClientRoot(t), tutorialInit{MapID: 1502, Tasks: tasks, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		if _, err := match.step(command{Ticks: 1}); err != nil {
			t.Fatal(err)
		}
	}
	xiaoqian, ok := match.actor(5)
	if !ok || xiaoqian.State != battleengine.ActorTrapped || xiaoqian.RoleID != 3 || xiaoqian.TeamID != tutorialTeam {
		t.Fatalf("actor 5: %+v (found %v)", xiaoqian, ok)
	}
	if left := xiaoqian.TrapExpiresAt - match.engine.ElapsedMS(); left < 2*tutorialTrapLongMS {
		t.Fatalf("trap ends in %d ms, want two long traps", left)
	}
	if !match.free() || match.tasks[0].Action != taskReach {
		t.Fatalf("record %+v, free %v: want free input until (12, 4)", match.tasks[0], match.free())
	}
	if err := match.engine.TeleportActor(1, battleengine.Cell{Row: 4, Col: 12}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := match.step(command{Ticks: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if match.free() || match.tasks[0].Action != taskWait {
		t.Fatalf("record %+v, free %v: want the next record", match.tasks[0], match.free())
	}
}

func TestTutorialItemHints(t *testing.T) {
	tasks := "1,27,6,8,4,8,4,0,3,0\r\n0,34,33,5,4,14,12,0,0,0\r\n0,36,1,0,0,0,0,100000,0,0\r\n"
	match, err := newTutorialMatch(offlineClientRoot(t), tutorialInit{MapID: tutorialItemMap, Tasks: tasks, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if _, err := match.step(command{Ticks: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if match.tasks[0].Action != taskEatItems || !match.free() {
		t.Fatalf("record %+v, want free input for the items", match.tasks[0])
	}
	match.pickup(1)
	match.pickup(1)
	if len(match.tasks) != 3 || match.tasks[0].Action != taskActorSay || match.tasks[0].Param != 16 || match.panelItem != 5 {
		t.Fatalf("after a candy: tasks %+v, panel %d", match.tasks, match.panelItem)
	}
	for index := 0; index < 3; index++ {
		if _, err := match.step(command{Ticks: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if match.hint == nil || match.hint.Text != 16 || match.hint.Avatar != 363 {
		t.Fatalf("hint %+v, want text 16 by misc363", match.hint)
	}
	for _, scene := range []uint32{2, 3, 4, 23, 24, 25, 61, 82} {
		match.pickup(scene)
	}
	if match.itemCount != tutorialItemKinds {
		t.Fatalf("%d kinds eaten, want 9", match.itemCount)
	}
}

func TestTutorialDefenceIsLostAtTheTopLeft(t *testing.T) {
	// 1504's enemy 3 with its routine: teleport to (13, 1), walk to (0, 1).
	tasks := "3,48,6,13,1,13,1,0,3,1\r\n0,49,25,5,1,5,1,0,3,1\r\n3,62,0,13,1,13,1,0,0,2\r\n3,63,26,13,1,13,1,0,3,0\r\n3,64,7,13,1,0,1,0,0,15\r\n0,36,10,0,0,0,0,0,0,311\r\n"
	match, err := newTutorialMatch(offlineClientRoot(t), tutorialInit{MapID: tutorialDefend, Tasks: tasks, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	for match.result == nil && match.engine.ElapsedMS() < 40_000 {
		if _, err := match.step(command{Ticks: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if match.result == nil || *match.result != tutorialResultLoss || match.begin.MapID != tutorialDefend {
		t.Fatalf("result %v on map %d at %d ms, want a loss on 1504", match.result, match.begin.MapID, match.engine.ElapsedMS())
	}
	if !match.routineMode || len(match.routines) != 1 || len(match.tasks) != 1 {
		t.Fatalf("routine mode %v, %d routines, tasks %+v", match.routineMode, len(match.routines), match.tasks)
	}
}
