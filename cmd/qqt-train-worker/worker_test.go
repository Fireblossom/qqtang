package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"qqtang/internal/game/battleenv"
	"qqtang/internal/game/mapdata"
)

// syntheticMap is an open 15x13 rule-1 arena with eight spawns, so the
// protocol tests run without client data.
func syntheticMap() mapdata.CompetitiveMap {
	const width, height = 15, 13
	field := mapdata.CompetitiveBattlefield{Width: width, Height: height, Cells: make([]mapdata.CompetitiveBattleCell, width*height)}
	for index := range field.Cells {
		field.Cells[index] = mapdata.CompetitiveBattleCell{Collision: mapdata.CompetitiveCellOpen, FlamePassable: true}
	}
	return mapdata.CompetitiveMap{
		ID: 2, Name: "synthetic", Selectable: true, PlayerLimit: 8,
		NativeRule: 1, Rule: mapdata.CompetitiveRuleOrdinary,
		SpawnGroupA: []mapdata.CompetitiveCell{{Row: 1, Col: 1}, {Row: 1, Col: 13}, {Row: 11, Col: 1}, {Row: 11, Col: 13}},
		SpawnGroupB: []mapdata.CompetitiveCell{{Row: 1, Col: 7}, {Row: 6, Col: 1}, {Row: 6, Col: 13}, {Row: 11, Col: 7}},
		Battlefield: field,
	}
}

func testMaps(t *testing.T, config Config) []mapdata.CompetitiveMap {
	t.Helper()
	if root := os.Getenv("QQT_TEST_CLIENT_ROOT"); root != "" {
		maps, err := config.SelectMaps(root)
		if err != nil {
			t.Fatal(err)
		}
		return maps
	}
	return []mapdata.CompetitiveMap{syntheticMap()}
}

// response is one decoded worker response.
type response struct {
	raw     []byte
	spatial []byte
	scalars []float32
	legal   []uint8
	active  []uint8
	reset   []uint8
	team    []uint8
	reward  []float32
	done    []uint8
	info    struct {
		Episodes []episodeInfo `json:"episodes"`
		Ticks    []int         `json:"ticks"`
	}
}

type protocolClient struct {
	t      *testing.T
	hello  Hello
	reader *bufio.Reader
	writer io.Writer
	slots  int
}

func startWorker(t *testing.T, config Config, maps []mapdata.CompetitiveMap) (*protocolClient, chan error) {
	t.Helper()
	worker, err := NewWorker(config, maps)
	if err != nil {
		t.Fatal(err)
	}
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := worker.Serve(requestReader, responseWriter, len(maps))
		responseWriter.CloseWithError(fmt.Errorf("worker stopped: %v", err))
		done <- err
	}()
	client := &protocolClient{t: t, reader: bufio.NewReader(responseReader), writer: requestWriter}
	line, err := client.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(line, &client.hello); err != nil {
		t.Fatal(err)
	}
	client.slots = client.hello.Envs * client.hello.Agents
	return client, done
}

func (client *protocolClient) request(op byte, payload []byte) {
	header := make([]byte, 9)
	binary.LittleEndian.PutUint32(header, requestMagic)
	header[4] = op
	binary.LittleEndian.PutUint32(header[5:], uint32(len(payload)))
	if _, err := client.writer.Write(append(header, payload...)); err != nil {
		client.t.Fatal(err)
	}
}

func (client *protocolClient) read() response {
	t := client.t
	var header [8]byte
	if _, err := io.ReadFull(client.reader, header[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(header[:4]) != responseMagic {
		t.Fatalf("response magic %#x", binary.LittleEndian.Uint32(header[:4]))
	}
	payload := make([]byte, binary.LittleEndian.Uint32(header[4:]))
	if _, err := io.ReadFull(client.reader, payload); err != nil {
		t.Fatal(err)
	}
	element := 4
	if client.hello.SpatialDtype == "f16" {
		element = 2
	}
	slots := client.slots
	result := response{raw: payload}
	offset := 0
	take := func(size int) []byte {
		if offset+size > len(payload) {
			t.Fatalf("response payload %d bytes ends before field at %d+%d", len(payload), offset, size)
		}
		part := payload[offset : offset+size]
		offset += size
		return part
	}
	floats := func(part []byte) []float32 {
		values := make([]float32, len(part)/4)
		for index := range values {
			values[index] = math.Float32frombits(binary.LittleEndian.Uint32(part[4*index:]))
		}
		return values
	}
	result.spatial = take(slots * client.hello.Channels * client.hello.Height * client.hello.Width * element)
	result.scalars = floats(take(slots * client.hello.Scalars * 4))
	result.legal = take(slots * client.hello.Actions)
	result.active = take(slots)
	result.reset = take(slots)
	result.team = take(slots)
	result.reward = floats(take(slots * 4))
	result.done = take(slots)
	infoLength := int(binary.LittleEndian.Uint32(take(4)))
	info := take(infoLength)
	if offset != len(payload) {
		t.Fatalf("response has %d trailing bytes", len(payload)-offset)
	}
	if err := json.Unmarshal(info, &result.info); err != nil {
		t.Fatalf("episode info %q: %v", info, err)
	}
	return result
}

func randomActions(random *rand.Rand, hello Hello, observation response) []byte {
	payload := make([]byte, 2*len(observation.active))
	for slot, active := range observation.active {
		action := int16(-1)
		if active != 0 {
			var legal []int16
			for id, allowed := range observation.legal[slot*hello.Actions : (slot+1)*hello.Actions] {
				if allowed != 0 {
					legal = append(legal, int16(id))
				}
			}
			action = legal[random.IntN(len(legal))]
		}
		binary.LittleEndian.PutUint16(payload[2*slot:], uint16(action))
	}
	return payload
}

func TestWorkerProtocolInProcess(t *testing.T) {
	for _, variant := range []struct{ dtype, cadence string }{{"f16", "fixed"}, {"f32", "fixed"}, {"f16", "server"}} {
		dtype, cadence := variant.dtype, variant.cadence
		t.Run(dtype+"_"+cadence, func(t *testing.T) {
			config, err := ParseConfig([]byte(fmt.Sprintf(`{"envs": 6, "player_counts": [2, 4, 8], "agents": 8,
				"team_layouts": [[1,2],[1,1,2,2],[1,2,3,4],[1,1,1,1,2,2,2,2]], "seed": 7, "workers": 3,
				"spatial_dtype": %q, "decision_cadence": %q, "random_phase": %t,
				"curriculum": {"duel": 0.2, "bomb_escape": 0.1}}`, dtype, cadence, cadence == "server")))
			if err != nil {
				t.Fatal(err)
			}
			client, done := startWorker(t, config, testMaps(t, config))
			hello := client.hello
			if hello.Protocol != 1 || hello.Envs != 6 || hello.Agents != 8 || hello.Channels != 117 || hello.Height != 13 ||
				hello.Width != 15 || hello.Scalars != 187 || hello.Actions != 45 || hello.TensorVersion != 18 || hello.SpatialDtype != dtype {
				t.Fatalf("hello = %+v", hello)
			}
			client.request(opReset, nil)
			observation := client.read()
			returns := make([]float64, client.slots)
			for slot := range observation.reset {
				if (observation.team[slot] != 0) != (observation.reset[slot] != 0) {
					t.Fatalf("RESET slot %d team %d reset %d", slot, observation.team[slot], observation.reset[slot])
				}
				if observation.reward[slot] != 0 || observation.done[slot] != 0 {
					t.Fatalf("RESET slot %d has reward/done", slot)
				}
			}
			random := rand.New(rand.NewPCG(1, 2))
			finished := 0
			for step := 0; step < 400; step++ {
				client.request(opStep, randomActions(random, hello, observation))
				previous := observation
				observation = client.read()
				episodesByEnv := map[int]episodeInfo{}
				for _, episode := range observation.info.Episodes {
					episodesByEnv[episode.Env] = episode
					finished++
				}
				for slot := range observation.active {
					env, agent := slot/hello.Agents, slot%hello.Agents
					legal := observation.legal[slot*hello.Actions : (slot+1)*hello.Actions]
					legalCount := 0
					for _, allowed := range legal {
						legalCount += int(allowed)
					}
					if (observation.active[slot] != 0) != (legalCount != 0) {
						t.Fatalf("step %d slot %d active %d with %d legal actions", step, slot, observation.active[slot], legalCount)
					}
					if observation.active[slot] != 0 && observation.team[slot] == 0 {
						t.Fatalf("step %d slot %d active without team", step, slot)
					}
					if previous.team[slot] == 0 && observation.reward[slot] != 0 {
						t.Fatalf("step %d padding slot %d earned %g", step, slot, observation.reward[slot])
					}
					returns[slot] += float64(observation.reward[slot])
					episode, ended := episodesByEnv[env]
					if (observation.done[slot] != 0) != (ended && agent < len(episode.Agents)) {
						t.Fatalf("step %d slot %d done=%d, episode reported=%t", step, slot, observation.done[slot], ended)
					}
					if ended && (observation.reset[slot] != 0) != (observation.team[slot] != 0) {
						t.Fatalf("step %d slot %d of a restarted env has reset=%d team=%d", step, slot, observation.reset[slot], observation.team[slot])
					}
					if !ended && observation.reset[slot] != 0 {
						t.Fatalf("step %d slot %d reset without a finished episode", step, slot)
					}
					if ended && agent < len(episode.Agents) {
						if diff := math.Abs(returns[slot] - float64(episode.Agents[agent].Return)); diff > 1e-3 {
							t.Fatalf("step %d slot %d summed rewards %g, episode return %g", step, slot, returns[slot], episode.Agents[agent].Return)
						}
						if episode.Agents[agent].Team != previous.team[slot] {
							t.Fatalf("episode team %d, slot team %d", episode.Agents[agent].Team, previous.team[slot])
						}
						returns[slot] = 0
					}
				}
				if cadence == "server" {
					if len(observation.info.Ticks) != hello.Envs {
						t.Fatalf("step %d: ticks %v", step, observation.info.Ticks)
					}
					for _, ticks := range observation.info.Ticks {
						if ticks < 1 || ticks > 5 {
							t.Fatalf("step %d: env advanced %d ticks", step, ticks)
						}
					}
				}
				for _, episode := range observation.info.Episodes {
					if episode.Rule != 1 || episode.Map == 0 || episode.Length == 0 || len(episode.Teams) < 2 {
						t.Fatalf("episode info %+v", episode)
					}
				}
			}
			if finished == 0 {
				t.Fatal("no episode finished in 400 random steps")
			}
			client.request(opClose, nil)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestWorkerIsDeterministic checks that the same seed and actions give
// byte-identical responses, independent of the goroutine count.
func TestWorkerIsDeterministic(t *testing.T) {
	run := func(workers int) [][]byte {
		config, err := ParseConfig([]byte(fmt.Sprintf(`{"envs": 8, "player_counts": [2, 4], "seed": 11,
			"workers": %d, "curriculum": {"duel": 0.25, "late_duel_replay": 0.2}}`, workers)))
		if err != nil {
			t.Fatal(err)
		}
		client, done := startWorker(t, config, testMaps(t, config))
		random := rand.New(rand.NewPCG(5, 6))
		client.request(opReset, nil)
		observation := client.read()
		responses := [][]byte{observation.raw}
		for step := 0; step < 150; step++ {
			client.request(opStep, randomActions(random, client.hello, observation))
			observation = client.read()
			responses = append(responses, observation.raw)
		}
		client.request(opReset, nil)
		responses = append(responses, client.read().raw)
		client.request(opClose, nil)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return responses
	}
	serial, parallel := run(1), run(8)
	for index := range serial {
		if !bytes.Equal(serial[index], parallel[index]) {
			t.Fatalf("response %d differs between 1 and 8 goroutines", index)
		}
	}
	again := run(4)
	for index := range serial {
		if !bytes.Equal(serial[index], again[index]) {
			t.Fatalf("response %d differs between runs", index)
		}
	}
}

func TestConfigRejectsUnknownKeysAndBadShares(t *testing.T) {
	for _, data := range []string{
		`{"envz": 4}`, `{"rule": 3}`, `{"spatial_dtype": "bf16"}`, `{"player_counts": [9]}`,
		`{"agents": 2, "player_counts": [4]}`, `{"curriculum": {"duel": 0.7, "blockade": 0.5}}`,
	} {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("config %s was accepted", data)
		}
	}
	config, err := ParseConfig([]byte(`{"rewards": {"trap": 0.3}}`))
	if err != nil {
		t.Fatal(err)
	}
	defaults := battleenv.DefaultRewardWeights()
	if config.Rewards.Trap != 0.3 || config.Rewards.Elimination != defaults.Elimination || config.Agents != 4 {
		t.Fatalf("partial reward override = %+v", config.Rewards)
	}
}
