package directory

import (
	"bytes"
	"encoding/hex"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func TestLocalKingdomPayloadMatchesOriginalEncoder(t *testing.T) {
	// Exported by the live v848 QQTEncoder, schema0806, from a15710-byte
	// native object. Importing the76 bytes reproduced the entire object.
	raw, err := os.ReadFile("testdata/kingdom_response_v848.hex")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultLocalHallNativeConfig()
	config.ServerPort = 58000
	got, err := BuildLocalKingdomPayload(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 76 || !bytes.Equal(got, want) {
		t.Fatalf("kingdom payload %x, native %x", got, want)
	}
	config.ServerIP, config.ServerPort = netip.MustParseAddr("192.0.2.7"), 19000
	got, err = BuildLocalKingdomPayload(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[66:70], []byte{7, 2, 0, 192}) || !bytes.Equal(got[74:], []byte{0x4a, 0x38}) {
		t.Fatalf("configured address/port was not encoded in native Winsock order: %x", got)
	}
}

func TestLocalKingdomRejectsUnusableNativeFields(t *testing.T) {
	for _, change := range []func(*LocalHallNativeConfig){
		func(c *LocalHallNativeConfig) { c.ServerPort = 0 },
		func(c *LocalHallNativeConfig) { c.ServerIP = netip.MustParseAddr("::1") },
		func(c *LocalHallNativeConfig) { c.LocationName = strings.Repeat("a", 41) },
		func(c *LocalHallNativeConfig) { c.LocationName = "a\x00b" },
	} {
		config := DefaultLocalHallNativeConfig()
		change(&config)
		if _, err := BuildLocalKingdomPayload(config); err == nil {
			t.Fatal("accepted unusable kingdom native fields")
		}
	}
}
