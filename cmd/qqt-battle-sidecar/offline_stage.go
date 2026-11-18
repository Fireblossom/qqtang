package main

import (
	"fmt"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// offlineStage is the server's half of an adventure (rule 12) stage.
type offlineStage struct {
	referee *offlineReferee
}

func newOfflineStage(clientRoot string, stageMap mapdata.AdventureMap) (*offlineStage, error) {
	return nil, fmt.Errorf("adventure stage %d: offline adventure is not supported yet", stageMap.ID)
}

func (stage *offlineStage) begin(options offlineInit, arbitrator uint16) (game.GameBeginData, error) {
	return game.GameBeginData{}, fmt.Errorf("not supported")
}

func (stage *offlineStage) attach(referee *offlineReferee) error {
	stage.referee = referee
	return nil
}

func (stage *offlineStage) observe(sender uint16, event game.GameEvent, now uint32) {}

func (stage *offlineStage) tick(now uint32) {}

func (stage *offlineStage) nextMap(sender uint16, event game.GameEvent) {}
