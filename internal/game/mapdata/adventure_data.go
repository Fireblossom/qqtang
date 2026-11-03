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
		if key = strings.TrimSpace(key); key == "mapCfgSize" || key == "pvedataCfgSize" {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				sizes[section] = n
			}
		}
	}
	return sizes, scanner.Err()
}

func readAdventureConfig(clientRoot, name string) ([]byte, error) {
	sizes, err := adventureConfigSizes(clientRoot)
	if err != nil {
		return nil, fmt.Errorf("read GameCFG.ini: %w", err)
	}
	size, ok := sizes[name]
	if !ok {
		return nil, fmt.Errorf("GameCFG.ini has no size for %s", name)
	}
	raw, err := os.ReadFile(filepath.Join(clientRoot, "config", name+".dat"))
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
