package mapdata

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// The adventure configuration files (config/pvedata.dat and
// config/battlefield_map<N>.dat) are read by QQTSection.dll's ReadCfg
// (0x1002BCDF): the bytes are unscrambled in place (0x1000BEC8: the last
// byte XORs the first, then each byte XORs its successor from the end),
// inflated with zlib into the size GameCFG.ini gives, cut at NUL, and CR LF
// becomes LF. client/native/tools/decrypt_pve.py runs the original under
// Unicorn and checks the same result.

// DecodeAdventureConfig turns one .dat file into the XML text Client.exe
// parses (still GB2312).
func DecodeAdventureConfig(raw []byte, size int) ([]byte, error) {
	b := append([]byte(nil), raw...)
	if n := len(b); n > 0 {
		b[n-1] ^= b[0]
		for i := n - 2; i >= 0; i-- {
			b[i] ^= b[i+1]
		}
	}
	reader, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("inflate adventure config: %w", err)
	}
	defer reader.Close()
	text, err := io.ReadAll(io.LimitReader(reader, int64(size)))
	if err != nil && len(text) == 0 {
		return nil, fmt.Errorf("inflate adventure config: %w", err)
	}
	if end := bytes.IndexByte(text, 0); end >= 0 {
		text = text[:end]
	}
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(text, []byte("\r"), []byte("\n")), nil
}

// adventureConfigSizes reads GameCFG.ini's mapCfgSize / pvedataCfgSize.
func adventureConfigSizes(clientRoot string) (map[string]int, error) {
	file, err := os.Open(filepath.Join(clientRoot, "config", "GameCFG.ini"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	sizes := map[string]int{}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			continue
		}
		if key = strings.TrimSpace(key); key == "mapCfgSize" || key == "pvedataCfgSize" || key == "propCfgSize" {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				sizes[section] = n
			}
		}
	}
	return sizes, scanner.Err()
}

func readAdventureConfig(clientRoot, name string, section ...string) ([]byte, error) {
	sizes, err := adventureConfigSizes(clientRoot)
	if err != nil {
		return nil, fmt.Errorf("read GameCFG.ini: %w", err)
	}
	key := name
	if len(section) > 0 {
		key = section[0]
	}
	size, ok := sizes[key]
	if !ok {
		return nil, fmt.Errorf("GameCFG.ini has no size for %s", name)
	}
	file := name + ".dat"
	if len(section) > 0 {
		file = name + ".ini"
	}
	raw, err := os.ReadFile(filepath.Join(clientRoot, "config", file))
	if err != nil {
		return nil, err
	}
	text, err := DecodeAdventureConfig(raw, size)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return simplifiedchinese.GBK.NewDecoder().Bytes(xmlDeclaration.ReplaceAll(text, []byte("<?xml version=\"1.0\"?>")))
}

var xmlDeclaration = regexp.MustCompile(`<\?xml[^>]*\?>`)

// AdventureLevel is one <lev_N> of pvedata: the attributes a player of
// that many courage points has in adventure (0x0054CD50 tagPVEDATA).
type AdventureLevel struct {
	Level   int
	Point   uint32
	BombNum byte
	BombPow uint16  // bubble damage (PVEBombPower)
	ReSpeed float64 // seconds per bubble refill (BombResumeSpeed)
	HP      uint32
	Speed   byte
}

// LoadAdventureLevels reads config/pvedata.dat, ordered by courage points.
func LoadAdventureLevels(clientRoot string) ([]AdventureLevel, error) {
	text, err := readAdventureConfig(clientRoot, "pvedata")
	if err != nil {
		return nil, err
	}
	var levels []AdventureLevel
	err = walkXML(text, func(name string, attrs map[string]string) {
		if !strings.HasPrefix(name, "lev_") {
			return
		}
		level, _ := strconv.Atoi(strings.TrimPrefix(name, "lev_"))
		levels = append(levels, AdventureLevel{Level: level, Point: uint32(atoi(attrs["point"])), BombNum: byte(atoi(attrs["bombnum"])),
			BombPow: uint16(atoi(attrs["bombpow"])), ReSpeed: atof(attrs["resspeed"]), HP: uint32(atoi(attrs["hp"])), Speed: byte(atoi(attrs["speed"]))})
	})
	return levels, err
}

// AdventureLevelFor returns the highest level the courage points reach.
func AdventureLevelFor(levels []AdventureLevel, points uint32) (AdventureLevel, bool) {
	var chosen AdventureLevel
	found := false
	for _, level := range levels {
		if points >= level.Point && (!found || level.Point >= chosen.Point) {
			chosen, found = level, true
		}
	}
	return chosen, found
}

// AdventureField is one BattleFields entry: ox is a column, oy a row.
type AdventureField struct{ Col, Row, W, H int }

type AdventureNPCBounds struct{ BeginRow, BeginCol, EndRow, EndCol int }

// AdventureNPCSkill keeps one Skills<N> element's attributes as written.
type AdventureNPCSkill map[string]string

// AdventureNPC is one NPCInfo of a stage (0x138C carries the same fields).
type AdventureNPC struct {
	NPCID          uint16
	IsBoss         bool
	NickName       string
	ImageID        uint32
	MoveStrategy   int
	Defense        int
	HP             int
	Speed          int
	Harm           int
	BattleStrategy int
	BornRow        int
	BornCol        int
	View           int
	Bounds         AdventureNPCBounds
	Skills         []AdventureNPCSkill
}

// AdventureStage is one config/battlefield_map<N>.dat.
type AdventureStage struct {
	ContinueID   int
	NextMapIndex uint32 // 0 on the last stage
	LevelRow     int    // the exit door cell
	LevelCol     int
	Fields       []AdventureField
	NPCs         []AdventureNPC
}

// LoadAdventureStage reads one stage's battlefield data.
func LoadAdventureStage(clientRoot string, mapID uint32) (AdventureStage, error) {
	text, err := readAdventureConfig(clientRoot, fmt.Sprintf("battlefield_map%d", mapID))
	if err != nil {
		return AdventureStage{}, err
	}
	var stage AdventureStage
	var npc *AdventureNPC
	err = walkXML(text, func(name string, attrs map[string]string) {
		switch {
		case name == "map":
			stage.ContinueID, stage.NextMapIndex = atoi(attrs["ContinueID"]), uint32(atoi(attrs["NextMapIndex"]))
			stage.LevelRow, stage.LevelCol = atoi(attrs["LevelRow"]), atoi(attrs["LevelCol"])
		case strings.HasPrefix(name, "Field"):
			stage.Fields = append(stage.Fields, AdventureField{Col: atoi(attrs["ox"]), Row: atoi(attrs["oy"]), W: atoi(attrs["w"]), H: atoi(attrs["h"])})
		case name == "NPCInfo":
			stage.NPCs = append(stage.NPCs, AdventureNPC{NPCID: uint16(atoi(attrs["NPCID"])), IsBoss: atoi(attrs["IsBoss"]) != 0,
				NickName: attrs["NickName"], ImageID: uint32(atoi(attrs["ImageID"])), MoveStrategy: atoi(attrs["MoveStrategy"]),
				Defense: atoi(attrs["Defense"]), HP: atoi(attrs["HP"]), Speed: atoi(attrs["Speed"]), Harm: atoi(attrs["Harm"]),
				BattleStrategy: atoi(attrs["BattleStrategy"]), BornRow: atoi(attrs["BornRow"]), BornCol: atoi(attrs["BornCol"]), View: atoi(attrs["View"])})
			npc = &stage.NPCs[len(stage.NPCs)-1]
		case name == "NPCBounds" && npc != nil:
			npc.Bounds = AdventureNPCBounds{atoi(attrs["BeginRow"]), atoi(attrs["BeginCol"]), atoi(attrs["EndRow"]), atoi(attrs["EndCol"])}
		case strings.HasPrefix(name, "Skills") && npc != nil:
			npc.Skills = append(npc.Skills, AdventureNPCSkill(attrs))
		}
	})
	return stage, err
}

func walkXML(text []byte, visit func(name string, attrs map[string]string)) error {
	decoder := xml.NewDecoder(bytes.NewReader(text))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if start, ok := token.(xml.StartElement); ok {
			attrs := make(map[string]string, len(start.Attr))
			for _, a := range start.Attr {
				attrs[a.Name.Local] = a.Value
			}
			visit(start.Name.Local, attrs)
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// AdventureProp is one <ID2xxxx> of config/errorPPP.ini's PropSet: the props
// adventure NPCs cast as skills (docs/adventure-npc-ai-re.md §6).
type AdventureProp struct {
	ID              uint32
	Name            string
	Tier            int
	InitialInterval uint32
	PrepareTime     uint32
	// Trap: present when the prop lies on the caster's cell for TrapTime.
	HasTrap  bool
	TrapTime uint32
	// Move: a projectile of MaxMoveDistance cells at MoveSpeed px/s.
	HasMove        bool
	MoveDistance   int
	MoveSpeed      int
	HitPlayerPower int
	IgniteBomb     bool
	AffectTime     uint32
	AffectCount    int
	Delay          uint32
	AffectShape    int
	AffectFactor   int
	AffectObject   int
	AffectState    int
	RestoreAttr    bool
	Effects        map[string]float64 // TriggerState children, e.g. HP -1500
	Anims          AdventurePropAnims
}

// AdventurePropAnims are the props' effect/*.eff names: PrepareAnim and
// its offset, TrapState.TrapEffect, MoveState.AnimID, and TriggerState's
// AffectAnim (on each target, with its offset) and GridsAnim (on each cell
// of the area).
type AdventurePropAnims struct {
	Prepare            string `json:"prepare,omitempty"`
	PrepareX, PrepareY int    `json:"-"`
	Trap               string `json:"trap,omitempty"`
	Move               string `json:"move,omitempty"`
	Affect             string `json:"affect,omitempty"`
	AffectX, AffectY   int    `json:"-"`
	Grids              string `json:"grids,omitempty"`
}

// adventurePropTiers is PropFireIntervalTable (ms by Tier).
var adventurePropTiers = map[int]uint32{1: 5000, 2: 10000, 3: 15000, 4: 30000, 5: 60000, 6: 150000, 100: 2000, 101: 4000, 102: 1000,
	103: 20000, 104: 8000, 105: 47000, 106: 47000, 107: 47000, 108: 47000, 109: 47000, 110: 47000, 111: 47000, 112: 47000, 113: 47000,
	114: 2000, 115: 2000, 116: 9000, 117: 3000, 118: 3000, 900: 3600000}

// FireInterval is the Tier's cooldown.
func (prop AdventureProp) FireInterval() uint32 { return adventurePropTiers[prop.Tier] }

// LoadAdventureProps reads config/errorPPP.ini. The client looks props up
// with sscanf("%*2s %ld") and takes the first match (0x005EA2F6), so an ID
// repeated later in the file is ignored.
func LoadAdventureProps(clientRoot string) (map[uint32]AdventureProp, error) {
	text, err := readAdventureConfig(clientRoot, "errorPPP", "prop")
	if err != nil {
		return nil, err
	}
	props := map[uint32]AdventureProp{}
	var current *AdventureProp
	inTrigger := false
	decoder := xml.NewDecoder(bytes.NewReader(text))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			name := t.Name.Local
			switch {
			case strings.HasPrefix(name, "ID") && len(name) > 2:
				id, convErr := strconv.Atoi(name[2:])
				if convErr != nil {
					current = nil
					continue
				}
				if _, seen := props[uint32(id)]; seen {
					current = nil
					continue
				}
				current = &AdventureProp{ID: uint32(id), Name: attrs["Name"], Tier: atoi(attrs["Tier"]), Effects: map[string]float64{}, AffectState: 1}
			case current == nil:
			case name == "PickedUpState":
				current.InitialInterval = uint32(atoi(attrs["InitialInterval"]))
			case name == "PrepareState":
				current.PrepareTime = uint32(atoi(attrs["PrepareTime"]))
				current.Anims.Prepare, current.Anims.PrepareX, current.Anims.PrepareY = attrs["PrepareAnim"], atoi(attrs["OffsetX"]), atoi(attrs["OffsetY"])
			case name == "TrapState":
				current.HasTrap, current.TrapTime = true, uint32(atoi(attrs["AffectTime"]))
				current.Anims.Trap = attrs["TrapEffect"]
			case name == "MoveState":
				current.HasMove, current.MoveDistance, current.MoveSpeed = true, atoi(attrs["MaxMoveDistance"]), atoi(attrs["MoveSpeed"])
				current.Anims.Move = attrs["AnimID"]
				current.HitPlayerPower, current.IgniteBomb = atoi(attrs["HitPlayerPower"]), atoi(attrs["IsIgniteBomb"]) != 0
			case name == "TriggerState":
				inTrigger = true
				current.AffectTime, current.AffectCount, current.Delay = uint32(atoi(attrs["AffectTime"])), atoi(attrs["AffectCount"]), uint32(atoi(attrs["Delay"]))
				current.AffectShape, current.AffectFactor, current.AffectObject = atoi(attrs["PropAffectShap"]), atoi(attrs["AffectFactor"]), atoi(attrs["PropAffectObject"])
				if state := atoi(attrs["PropAffectState"]); state != 0 {
					current.AffectState = state
				}
				current.RestoreAttr = atoi(attrs["RestorePlayerAttr"]) != 0
				current.Anims.Affect, current.Anims.Grids = attrs["AffectAnim"], attrs["GridsAnim"]
				current.Anims.AffectX, current.Anims.AffectY = atoi(attrs["AffectAnimOffsetX"]), atoi(attrs["AffectAnimOffsetY"])
			case inTrigger:
				var value string
				if decoder.DecodeElement(&value, &t) == nil {
					current.Effects[name] = atof(value)
				}
			}
		case xml.EndElement:
			switch {
			case t.Name.Local == "TriggerState":
				inTrigger = false
			case current != nil && strings.HasPrefix(t.Name.Local, "ID") && t.Name.Local == fmt.Sprintf("ID%d", current.ID):
				props[current.ID] = *current
				current = nil
			}
		}
	}
	return props, nil
}
