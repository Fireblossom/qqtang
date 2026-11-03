package mapdata

import (
	"os"
	"testing"
)

func TestLoadMapLayersFindsTheField15Doors(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	m, err := LoadMapLayers(root, "field15_8.map")
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 15 || m.Height != 13 || m.Element(1, 1, 1) != 17029 || m.Element(1, 11, 13) != 17029 || m.Element(1, 0, 0) != 17024 {
		t.Fatalf("%dx%d (1,1)=%d (0,0)=%d", m.Width, m.Height, m.Element(1, 1, 1), m.Element(1, 0, 0))
	}
}
