package mapdata

import (
	"encoding/binary"
	"fmt"
)

// 00618EF5 queries the third layer's runtime pointer grid, then virtual
// +24(6). For the shipped supply floor 13015, 005EF340 tests canMove & 16.
func parseMachineSupplyCells(elements map[uint32]competitiveMapElement, data []byte) ([]CompetitiveCell, error) {
	header, width, height, err := competitiveMapGeometry(data)
	if err != nil {
		return nil, err
	}
	end := header + 3*int(width*height)*4
	if end > len(data) || width > 15 || height > 13 {
		return nil, fmt.Errorf("machine supply floor is truncated or exceeds 15x13")
	}
	// Project positive anchors exactly as the layer manager does. Orphan
	// negative editor markers create no object; later anchors can overwrite.
	type projectedFloor struct {
		element *competitiveMapElement
		anchor  CompetitiveCell
	}
	floor := make([]projectedFloor, int(width*height))
	base := header + 2*int(width*height)*4
	for row := 0; row < int(height); row++ {
		for col := 0; col < int(width); col++ {
			id := int32(binary.LittleEndian.Uint32(data[base+(row*int(width)+col)*4:]))
			if id <= 0 {
				continue
			}
			element, known := elements[uint32(id)]
			if !known {
				return nil, fmt.Errorf("unknown machine floor element %d", id)
			}
			for dy := 0; dy < element.height && row+dy < int(height); dy++ {
				for dx := 0; dx < element.width && col+dx < int(width); dx++ {
					floor[(row+dy)*int(width)+col+dx] = projectedFloor{&element, CompetitiveCell{Row: byte(row), Col: byte(col)}}
				}
			}
		}
	}
	var cells []CompetitiveCell
	for index, projected := range floor {
		col := index % int(width)
		if projected.element != nil && projected.element.canMove&16 != 0 && (col < 6 || col > 8) {
			cells = append(cells, projected.anchor)
		}
	}
	return cells, nil
}
