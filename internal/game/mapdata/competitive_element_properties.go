package mapdata

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The shipped Python catalog omits the tank elements (19030..19054), although
// Client.exe loads object/mapElem/mapElem.prop through 005C37AF. Keep explicit
// Python definitions and fill only missing IDs from the native property file.
// All 721 definitions shared by the installed catalogs agree on these fields.
func supplementCompetitiveMapElements(scriptPath string, elements map[uint32]competitiveMapElement) error {
	path := strings.TrimSuffix(scriptPath, filepath.Ext(scriptPath)) + ".prop"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read map-element properties: %w", err)
	}
	properties, err := parseCompetitiveMapElementProperties(data)
	if err != nil {
		return err
	}
	for id, element := range properties {
		if _, exists := elements[id]; !exists {
			elements[id] = element
		}
	}
	return nil
}

// Version 2 has an eight-byte version/count header, followed by records with a
// 24-byte fixed part and width*height uint32 grid attributes, all little-endian.
// The fixed fields are ID, int16 width/height/offsetX/offsetY, int32 lifetime,
// flags and canMove. Drawing offsets and flags are not needed by the engine.
func parseCompetitiveMapElementProperties(data []byte) (map[uint32]competitiveMapElement, error) {
	if len(data) < 8 || binary.LittleEndian.Uint32(data) != 2 {
		return nil, fmt.Errorf("map-element properties require version 2 header")
	}
	count := binary.LittleEndian.Uint32(data[4:])
	elements := make(map[uint32]competitiveMapElement)
	pos := 8
	for index := uint32(0); index < count; index++ {
		if len(data)-pos < 24 {
			return nil, fmt.Errorf("map-element properties record %d is truncated", index)
		}
		record := data[pos:]
		id := binary.LittleEndian.Uint32(record)
		width := int(int16(binary.LittleEndian.Uint16(record[4:])))
		height := int(int16(binary.LittleEndian.Uint16(record[6:])))
		life := int(int32(binary.LittleEndian.Uint32(record[12:])))
		if width < 1 || height < 1 || width > 255 || height > 255 || life > 255 {
			return nil, fmt.Errorf("map element %d has unsupported properties size %dx%d lifetime %d", id, width, height, life)
		}
		size := 24 + width*height*4
		if len(record) < size {
			return nil, fmt.Errorf("map element %d has truncated grid attributes", id)
		}
		attrs := make([]uint32, width*height)
		for cell := range attrs {
			attrs[cell] = binary.LittleEndian.Uint32(record[24+cell*4:])
		}
		elements[id] = competitiveMapElement{
			id: id, width: width, height: height, lifeTime: life,
			canMove: binary.LittleEndian.Uint32(record[20:]), gridAttrs: attrs,
		}
		pos += size
	}
	if pos != len(data) {
		return nil, fmt.Errorf("map-element properties have %d trailing bytes", len(data)-pos)
	}
	return elements, nil
}
