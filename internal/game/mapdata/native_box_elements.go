package mapdata

import "path/filepath"

// NativeBoxElement exposes the native element fields rule 8 needs for dynamic
// boxes. Flags is canMove: bits 4/8/16 select fire, ice and rebound behaviour.
// The regular battlefield projection deliberately keeps only traversal.
type NativeBoxElement struct {
	ID            uint32
	LifeTime      int
	Flags         uint32
	Width, Height int
	GridAttrs     []uint32
}

func LoadNativeBoxElements(clientRoot string) (map[uint32]NativeBoxElement, error) {
	elements, err := loadCompetitiveMapElements(filepath.Join(clientRoot, "object", "mapElem", "mapElem.py"))
	if err != nil {
		return nil, err
	}
	out := make(map[uint32]NativeBoxElement, len(elements))
	for id, e := range elements {
		out[id] = NativeBoxElement{ID: id, LifeTime: e.lifeTime, Flags: e.canMove, Width: e.width, Height: e.height, GridAttrs: append([]uint32(nil), e.gridAttrs...)}
	}
	return out, nil
}
