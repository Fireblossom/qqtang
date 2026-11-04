package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
)

func TestNPCWanderCacheMatchesNativeController(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_follow_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	checked := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row struct {
			Kind     string
			Case     string
			Position [2]float64
			Facing   byte
			Result   byte
			AI       struct {
				Cache           [2]int16
				Blocked         int
				CachedDirection byte `json:"cached_direction"`
				Mask            uint32
				Random          uint32
				Gate            int
				Danger          int
			}
			AfterCache           [2]int16 `json:"after_cache"`
			AfterCachedDirection byte     `json:"after_cached_direction"`
			RandomCalls          int      `json:"random_calls"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind != "wander_follow" || row.AI.Gate == 0 || row.AI.Danger != 0 || row.Case == "path_outside_centre_follows" {
			continue
		}
		t.Run(row.Case, func(t *testing.T) {
			cache := npcWanderCache{cell: battleengine.Cell{Row: row.AI.Cache[0], Col: row.AI.Cache[1]}, direction: row.AI.CachedDirection, valid: true}
			cell := battleengine.Cell{Row: int16(int(row.Position[1]) / 40), Col: int16(int(row.Position[0]) / 40)}
			calls := 0
			got := cache.choose(cell, row.AI.Blocked != 0, row.AI.Mask, row.Facing, func() uint32 {
				calls++
				return row.AI.Random
			})
			if got != row.Result || calls != row.RandomCalls || cache.direction != row.AfterCachedDirection || cache.cell != (battleengine.Cell{Row: row.AfterCache[0], Col: row.AfterCache[1]}) {
				t.Fatalf("direction%d calls%d cache%+v; native direction%d calls%d cell%v cached%d", got, calls, cache, row.Result, row.RandomCalls, row.AfterCache, row.AfterCachedDirection)
			}
		})
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 8 {
		t.Fatalf("native cache cases%d", checked)
	}
}
