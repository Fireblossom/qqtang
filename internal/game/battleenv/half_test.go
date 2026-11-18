package battleenv

import (
	"math"
	"testing"
)

func TestFloat32ToHalfMatchesIEEE(t *testing.T) {
	cases := []struct {
		value float32
		half  uint16
	}{
		{0, 0x0000}, {float32(math.Copysign(0, -1)), 0x8000}, {1, 0x3c00}, {-2, 0xc000},
		{0.5, 0x3800}, {65504, 0x7bff}, {65520, 0x7c00}, {1e6, 0x7c00},
		{float32(math.Inf(1)), 0x7c00}, {float32(math.Inf(-1)), 0xfc00},
		{6.103515625e-05, 0x0400},                                                // smallest normal
		{5.960464477539063e-08, 1},                                               // smallest subnormal
		{2.9802322387695312e-08, 0},                                              // half of it: ties to even
		{1.0009765625, 0x3c01}, {1.00048828125, 0x3c00}, {1.00146484375, 0x3c02}, // ties to even
		{0.1, 0x2e66}, {1.0 / 3.0, 0x3555},
	}
	for _, test := range cases {
		if got := Float32ToHalf(test.value); got != test.half {
			t.Errorf("Float32ToHalf(%g) = %#04x, want %#04x", test.value, got, test.half)
		}
	}
	if got := Float32ToHalf(float32(math.NaN())); got&0x7c00 != 0x7c00 || got&0x3ff == 0 {
		t.Errorf("NaN converted to %#04x", got)
	}
	for half := 0; half < 0x7c00; half++ {
		if got := Float32ToHalf(HalfToFloat32(uint16(half))); got != uint16(half) {
			t.Fatalf("round trip %#04x -> %g -> %#04x", half, HalfToFloat32(uint16(half)), got)
		}
	}
}
