package battleenv

import "math"

// Float32ToHalf converts to IEEE 754 binary16 with round-to-nearest-even,
// the same result as numpy's float32 -> float16 cast (overflow -> inf,
// NaN stays NaN, subnormals are kept).
func Float32ToHalf(value float32) uint16 {
	bits := math.Float32bits(value)
	sign := uint16(bits>>16) & 0x8000
	exponent := int32(bits>>23) & 0xff
	mantissa := bits & 0x7fffff
	switch {
	case exponent == 0xff:
		if mantissa != 0 {
			return sign | 0x7e00
		}
		return sign | 0x7c00
	case exponent > 142: // >= 2^16 overflows half
		return sign | 0x7c00
	case exponent >= 113: // normal half
		halfExponent := uint32(exponent-112) << 10
		result := halfExponent | mantissa>>13
		remainder := mantissa & 0x1fff
		if remainder > 0x1000 || (remainder == 0x1000 && result&1 != 0) {
			result++ // may carry into the exponent, up to infinity: correct
		}
		return sign | uint16(result)
	case exponent >= 102: // subnormal half
		mantissa |= 0x800000
		shift := uint32(126 - exponent)
		result := mantissa >> shift
		remainder := mantissa & (1<<shift - 1)
		halfway := uint32(1) << (shift - 1)
		if remainder > halfway || (remainder == halfway && result&1 != 0) {
			result++
		}
		return sign | uint16(result)
	default:
		return sign
	}
}

// HalfToFloat32 is the exact inverse of Float32ToHalf for finite values.
func HalfToFloat32(half uint16) float32 {
	sign := uint32(half&0x8000) << 16
	exponent := uint32(half>>10) & 0x1f
	mantissa := uint32(half & 0x3ff)
	switch {
	case exponent == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | mantissa<<13)
	case exponent != 0:
		return math.Float32frombits(sign | (exponent+112)<<23 | mantissa<<13)
	case mantissa == 0:
		return math.Float32frombits(sign)
	default:
		value := float32(mantissa) / 1024 / 16384
		if sign != 0 {
			return -value
		}
		return value
	}
}

// ConvertToHalf converts source into destination (same length). Observation
// planes are mostly exact zeros and ones, which take a fast path.
func ConvertToHalf(destination []uint16, source []float32) {
	destination = destination[:len(source)]
	for index, value := range source {
		switch math.Float32bits(value) {
		case 0:
			destination[index] = 0
		case 0x3f800000:
			destination[index] = 0x3c00
		default:
			destination[index] = Float32ToHalf(value)
		}
	}
}
