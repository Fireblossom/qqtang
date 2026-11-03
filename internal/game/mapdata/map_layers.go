package mapdata

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// MapLayers is the raw element IDs of a .map file: Layers[0] is the top
// layer, [1] the wall layer, [2] the floor layer, each row-major.
type MapLayers struct {
	Width, Height int
	Layers        [3][]int32
}

// Element returns the element ID of one layer cell, 0 outside the map.
func (m MapLayers) Element(layer, row, col int) int32 {
	if row < 0 || col < 0 || row >= m.Height || col >= m.Width {
		return 0
	}
	return m.Layers[layer][row*m.Width+col]
}

// LoadMapLayers reads the three element layers of map/<fileName>.
func LoadMapLayers(clientRoot, fileName string) (MapLayers, error) {
	data, err := os.ReadFile(filepath.Join(clientRoot, "map", fileName))
	if err != nil {
		return MapLayers{}, err
	}
	header, width, height, err := competitiveMapGeometry(data)
	if err != nil {
		return MapLayers{}, err
	}
	cells := int(width * height)
	if header+3*cells*4 > len(data) {
		return MapLayers{}, fmt.Errorf("map %s layers are truncated", fileName)
	}
	m := MapLayers{Width: int(width), Height: int(height)}
	for layer := range m.Layers {
		m.Layers[layer] = make([]int32, cells)
		for i := range cells {
			m.Layers[layer][i] = int32(binary.LittleEndian.Uint32(data[header+(layer*cells+i)*4:]))
		}
	}
	return m, nil
}
