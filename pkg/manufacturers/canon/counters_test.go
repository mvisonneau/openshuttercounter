package canon

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both halves of the differential experiment: every MPU reply captured before
// and after exactly twenty frames were taken.
var capturedReplies = []struct {
	address       uint32
	label         string
	before, after string
	wantBefore    uint32
	wantAfter     uint32
}{
	{0x1001, "shutter", "0066000000000000f200", "00670000000000000006", 26354, 26374},
	{0x100b, "mirror", "00660000000000001000", "00660000000000002400", 26128, 26148},
	{0x1015, "total", "006d0000000000820000", "006d0000000000960000", 28034, 28054},
}

func TestDecodeCounterReproducesCaptures(t *testing.T) {
	for _, tc := range capturedReplies {
		t.Run(tc.label, func(t *testing.T) {
			before, err := DecodeCounter(mustHex(t, tc.before))
			require.NoError(t, err)

			after, err := DecodeCounter(mustHex(t, tc.after))
			require.NoError(t, err)

			assert.Equal(t, tc.wantBefore, before)
			assert.Equal(t, tc.wantAfter, after)

			// Twenty frames were taken between the two captures, so every
			// counter must have advanced by exactly twenty.
			assert.Equal(t, uint32(20), after-before)
		})
	}
}

// TestDecodeCounterCarry is the evidence for the encoding rather than a test
// of it. At MPU 0x1001 the low byte read 242 and 242 + 20 = 262 overflows a
// byte, so the high byte at offset 1 had to step from 0x66 to 0x67. It did. A
// misread format would not carry correctly.
func TestDecodeCounterCarry(t *testing.T) {
	before := mustHex(t, "0066000000000000f200")
	after := mustHex(t, "00670000000000000006")

	require.Equal(t, byte(0x66), before[1])
	require.Equal(t, byte(0x67), after[1])

	b, err := DecodeCounter(before)
	require.NoError(t, err)

	a, err := DecodeCounter(after)
	require.NoError(t, err)

	assert.Equal(t, uint32(20), a-b)
}

// TestDecodeCounterLowByteOffsetVaries pins the one part of the format that is
// not understood: the low byte turns up at offset 7, 8 or 9 depending on the
// reply, which is why it is taken as the last non-zero byte rather than read
// from a fixed position.
func TestDecodeCounterLowByteOffsetVaries(t *testing.T) {
	offsets := map[int]bool{}

	for _, tc := range capturedReplies {
		for _, sample := range []string{tc.before, tc.after} {
			raw := mustHex(t, sample)

			for i := len(raw) - 1; i >= 2; i-- {
				if raw[i] != 0 {
					offsets[i] = true

					break
				}
			}
		}
	}

	assert.Greater(t, len(offsets), 1, "the low byte offset is expected to vary across captures")
}

func TestDecodeCounterRejectsShortReplies(t *testing.T) {
	_, err := DecodeCounter([]byte{0x00, 0x66})
	assert.Error(t, err)
}

func TestLookupModel(t *testing.T) {
	m, ok := LookupModel(0x3281)
	require.True(t, ok)
	assert.Equal(t, "EOS 5D Mark IV", m.Name)
	assert.True(t, m.Confirmed)
	assert.Len(t, m.Counters, 3)

	_, ok = LookupModel(0xffff)
	assert.False(t, ok)
}

func TestFallbackModelIsNotClaimedAsConfirmed(t *testing.T) {
	m := FallbackModel(0x1234)
	assert.False(t, m.Confirmed)
	assert.NotEmpty(t, m.Counters)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	require.NoError(t, err)

	return b
}
