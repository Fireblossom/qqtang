package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"unsafe"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/battleenv"
	"qqtang/internal/game/mapdata"
)

// Wire constants of training/README.md, "Worker protocol".
const (
	protocolVersion = 1
	requestMagic    = 0x57545151 // "QQTW" little-endian
	responseMagic   = 0x52545151 // "QQTR" little-endian
	opReset         = 1
	opStep          = 2
	opClose         = 3
	actionCount     = int(battleengine.DiscreteActionCount)
)

// Hello is the first stdout line.
type Hello struct {
	Protocol      int    `json:"protocol"`
	Envs          int    `json:"envs"`
	Agents        int    `json:"agents"`
	Channels      int    `json:"channels"`
	Height        int    `json:"height"`
	Width         int    `json:"width"`
	Scalars       int    `json:"scalars"`
	Actions       int    `json:"actions"`
	TensorVersion uint16 `json:"tensor_version"`
	SpatialDtype  string `json:"spatial_dtype"`
	Cadence       string `json:"decision_cadence"`
	Rule          int    `json:"rule"`
	Maps          int    `json:"maps"`
	DecisionMS    int    `json:"decision_ms"`
}

// Worker owns one vectorized environment and its reusable response buffers.
type Worker struct {
	config Config
	batch  *battleenv.Batch
	envs   int
	agents int
	slots  int
	f16    bool

	header      [8]byte
	small       []byte // legal, active, reset, team
	rewards     []float32
	dones       []uint8
	actions     []int16
	actionBytes []byte
	info        []byte
	// current aliases the environment's reusable observation buffers; it is
	// valid until the next Reset/Step.
	current battleenv.TensorBatch

	// Counters for the -bench report.
	Decisions uint64
	Steps     uint64
}

// NewWorker builds the environment for config over the given map pool.
func NewWorker(config Config, maps []mapdata.CompetitiveMap) (*Worker, error) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		return nil, fmt.Errorf("the worker writes host-order floats and needs a little-endian CPU")
	}
	batch, err := battleenv.NewBatch(config.BatchConfig(maps))
	if err != nil {
		return nil, err
	}
	if height, width := batch.TensorShape(); height != tensorHeight || width != tensorWidth {
		return nil, fmt.Errorf("environment tensor %dx%d, want %dx%d", height, width, tensorHeight, tensorWidth)
	}
	slots := config.Envs * config.Agents
	worker := &Worker{
		config: config, batch: batch, envs: config.Envs, agents: config.Agents, slots: slots,
		f16:     config.SpatialDtype == "f16",
		small:   make([]byte, slots*(actionCount+3)),
		rewards: make([]float32, slots), dones: make([]uint8, slots),
		actions: make([]int16, slots), actionBytes: make([]byte, slots*2),
	}
	return worker, nil
}

// Hello describes the stream to the trainer.
func (worker *Worker) Hello(mapCount int) Hello {
	return Hello{
		Protocol: protocolVersion, Envs: worker.envs, Agents: worker.agents,
		Channels: tensorChannels, Height: tensorHeight, Width: tensorWidth,
		Scalars: battleenv.ScalarFeatures, Actions: actionCount,
		TensorVersion: battleenv.TensorSchemaVersion, SpatialDtype: worker.config.SpatialDtype,
		Cadence: worker.config.DecisionCadence,
		Rule:    worker.config.Rule, Maps: mapCount, DecisionMS: decisionMS,
	}
}

// ResponseBytes is the payload size of one RESET/STEP response without the
// episode-info JSON.
func (worker *Worker) ResponseBytes() int {
	spatialElement := 4
	if worker.f16 {
		spatialElement = 2
	}
	return worker.slots*(tensorChannels*tensorHeight*tensorWidth*spatialElement+battleenv.ScalarFeatures*4+actionCount+3+4+1) + 4
}

// Serve runs the request loop until CLOSE or end of input.
func (worker *Worker) Serve(input io.Reader, output io.Writer, mapCount int) error {
	reader := bufio.NewReaderSize(input, 1<<16)
	writer := bufio.NewWriterSize(output, 1<<20)
	hello, err := json.Marshal(worker.Hello(mapCount))
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(hello, '\n')); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	var header [9]byte
	for {
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read request header: %w", err)
		}
		if magic := binary.LittleEndian.Uint32(header[0:4]); magic != requestMagic {
			return fmt.Errorf("request magic %#x, want %#x", magic, requestMagic)
		}
		op := header[4]
		length := int(binary.LittleEndian.Uint32(header[5:9]))
		switch op {
		case opReset:
			if length != 0 {
				return fmt.Errorf("RESET payload %d bytes, want 0", length)
			}
			if err := worker.Reset(); err != nil {
				return err
			}
		case opStep:
			if length != len(worker.actionBytes) {
				return fmt.Errorf("STEP payload %d bytes, want %d", length, len(worker.actionBytes))
			}
			if _, err := io.ReadFull(reader, worker.actionBytes); err != nil {
				return fmt.Errorf("read STEP actions: %w", err)
			}
			for index := range worker.actions {
				worker.actions[index] = int16(binary.LittleEndian.Uint16(worker.actionBytes[2*index:]))
			}
			if err := worker.Step(worker.actions); err != nil {
				return err
			}
		case opClose:
			if length != 0 {
				return fmt.Errorf("CLOSE payload %d bytes, want 0", length)
			}
			return writer.Flush()
		default:
			return fmt.Errorf("unknown request op %d", op)
		}
		if err := worker.WriteResponse(writer); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
}

// Reset restarts every environment.
func (worker *Worker) Reset() error {
	if err := worker.batch.ResetAll(); err != nil {
		return err
	}
	tensors, err := worker.batch.Observe()
	if err != nil {
		return err
	}
	clear(worker.rewards)
	clear(worker.dones)
	worker.fillSmall(tensors, nil, true)
	worker.info = append(worker.info[:0], `{"episodes":[]}`...)
	return worker.checkSpatial(tensors)
}

// Step applies one decision per slot (-1 for slots that do not decide).
func (worker *Worker) Step(actions []int16) error {
	result, err := worker.batch.StepFlat(actions)
	if err != nil {
		return err
	}
	copy(worker.rewards, result.Rewards)
	clear(worker.dones)
	for envIndex, done := range result.Dones {
		if done == 0 {
			continue
		}
		for _, completed := range result.Completed {
			if completed.EnvIndex == envIndex {
				for slot := range completed.PlayerIDs {
					worker.dones[envIndex*worker.agents+slot] = 1
				}
			}
		}
	}
	worker.fillSmall(result.Observation, result.Resets, false)
	var ticks []uint8
	if worker.config.DecisionCadence == "server" {
		ticks = result.Ticks
	}
	worker.info, err = appendEpisodeInfo(worker.info[:0], result.Completed, ticks)
	if err != nil {
		return err
	}
	for _, active := range worker.small[worker.slots*actionCount : worker.slots*(actionCount+1)] {
		worker.Decisions += uint64(active)
	}
	worker.Steps++
	return worker.checkSpatial(result.Observation)
}

// fillSmall packs legal, active, reset and team (u8 rows) in wire order.
func (worker *Worker) fillSmall(tensors battleenv.TensorBatch, resets []uint8, all bool) {
	legal := worker.small[:worker.slots*actionCount]
	active := worker.small[worker.slots*actionCount : worker.slots*(actionCount+1)]
	reset := worker.small[worker.slots*(actionCount+1) : worker.slots*(actionCount+2)]
	team := worker.small[worker.slots*(actionCount+2) : worker.slots*(actionCount+3)]
	copy(legal, tensors.Legal)
	copy(active, tensors.DecisionReady)
	copy(team, tensors.TeamIDs)
	clear(reset)
	if all {
		for slot, teamID := range tensors.TeamIDs {
			if teamID != 0 {
				reset[slot] = 1
			}
		}
	} else {
		copy(reset, resets)
	}
	worker.current = tensors
}

// checkSpatial verifies the observation tensor size before it is sent.
func (worker *Worker) checkSpatial(tensors battleenv.TensorBatch) error {
	want := worker.slots * tensorChannels * tensorHeight * tensorWidth
	got := len(tensors.Spatial)
	if worker.f16 {
		got = len(tensors.SpatialHalf)
	}
	if got != want {
		return fmt.Errorf("spatial tensor has %d values, want %d", got, want)
	}
	return nil
}

func uint16Bytes(values []uint16) []byte {
	if len(values) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&values[0])), len(values)*2)
}

func float32Bytes(values []float32) []byte {
	if len(values) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&values[0])), len(values)*4)
}

// WriteResponse writes the framed response for the last Reset/Step.
func (worker *Worker) WriteResponse(writer io.Writer) error {
	tensors := worker.current
	payload := worker.ResponseBytes() + len(worker.info)
	binary.LittleEndian.PutUint32(worker.header[0:4], responseMagic)
	binary.LittleEndian.PutUint32(worker.header[4:8], uint32(payload))
	var infoLength [4]byte
	binary.LittleEndian.PutUint32(infoLength[:], uint32(len(worker.info)))
	spatial := float32Bytes(tensors.Spatial)
	if worker.f16 {
		spatial = uint16Bytes(tensors.SpatialHalf)
	}
	smallRows := worker.small[:worker.slots*(actionCount+3)]
	parts := [][]byte{
		worker.header[:], spatial, float32Bytes(tensors.Scalars), smallRows,
		float32Bytes(worker.rewards), worker.dones, infoLength[:], worker.info,
	}
	for _, part := range parts {
		if _, err := writer.Write(part); err != nil {
			return err
		}
	}
	return nil
}
