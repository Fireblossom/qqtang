package mapdata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func propertyFixture(elements ...competitiveMapElement) []byte {
	data := binary.LittleEndian.AppendUint32(nil, 2)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(elements)))
	for _, element := range elements {
		header := make([]byte, 24)
		binary.LittleEndian.PutUint32(header, element.id)
		binary.LittleEndian.PutUint16(header[4:], uint16(element.width))
		binary.LittleEndian.PutUint16(header[6:], uint16(element.height))
		binary.LittleEndian.PutUint32(header[12:], uint32(element.lifeTime))
		binary.LittleEndian.PutUint32(header[20:], element.canMove)
		data = append(data, header...)
		for _, attr := range element.gridAttrs {
			data = binary.LittleEndian.AppendUint32(data, attr)
		}
	}
	return data
}

func TestNativePropertiesSupplyMissingTankWalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapElem.py")
	if err := os.WriteFile(path, []byte("class QQTMapElem1(QQTMapElem):\n    LifeTime = 2\n    GridAttr = (0,)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data := propertyFixture(
		competitiveMapElement{id: 1, width: 1, height: 1, lifeTime: -1, gridAttrs: []uint32{0}},
		competitiveMapElement{id: 19033, width: 1, height: 1, lifeTime: 1, gridAttrs: []uint32{0}},
		competitiveMapElement{id: 19031, width: 3, height: 3, lifeTime: -1, gridAttrs: []uint32{0, 0x1f1f, 0, 0x1f1f, 0x1f1f, 0x1f1f, 0, 0x1f1f, 0}},
	)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "mapElem.prop"), data, 0600); err != nil {
		t.Fatal(err)
	}
	elements, err := loadCompetitiveMapElements(path)
	if err != nil {
		t.Fatal(err)
	}
	if elements[1].lifeTime != 2 {
		t.Fatal("native fallback overwrote an explicit Python definition")
	}
	contents := testCompetitiveMapBytes(1, nil)
	binary.LittleEndian.PutUint32(contents[12+(2*competitiveMapWidthV3+3)*4:], 19033)
	field, err := parseCompetitiveBattlefield(elements, contents)
	if err != nil {
		t.Fatal(err)
	}
	wall, _ := field.Cell(2, 3)
	if wall.Collision != CompetitiveCellBreakable || wall.Durability != 1 || wall.MapElementID != 19033 {
		t.Fatalf("native tank wall lost destruction identity: %+v", wall)
	}
	base := elements[19031]
	if base.width != 3 || base.height != 3 || base.gridAttrs[0] != 0 || base.gridAttrs[4] != 0x1f1f || base.gridAttrs[8] != 0 {
		t.Fatalf("base footprint = %+v", base)
	}
}

func TestNativePropertiesRejectIncompleteFootprints(t *testing.T) {
	data := propertyFixture(competitiveMapElement{id: 1, width: 2, height: 1, lifeTime: 1, canMove: 6, gridAttrs: []uint32{0, 0x1515}})
	elements, err := parseCompetitiveMapElementProperties(data)
	if err != nil || elements[1].canMove != 6 || elements[1].gridAttrs[1] != 0x1515 {
		t.Fatalf("native directional movable footprint = %+v, %v", elements, err)
	}
	for _, truncated := range [][]byte{data[:7], data[:20], data[:len(data)-1]} {
		if _, err := parseCompetitiveMapElementProperties(truncated); err == nil {
			t.Fatal("accepted a truncated property catalog")
		}
	}
}
