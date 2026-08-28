package usb

import "math"

// clampUint32 narrows a millisecond timeout to the width the platform's
// transfer call takes, saturating rather than wrapping so a very long timeout
// stays very long instead of becoming a very short one.
func clampUint32(v int64) uint32 {
	switch {
	case v < 0:
		return 0
	case v > math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(v)
	}
}
