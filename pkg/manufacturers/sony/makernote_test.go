package sony

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encipher is Sony's forward cipher, c = b³ mod 249, written out
// independently of the decipher table it is checking.
func encipher(in []byte) []byte {
	out := make([]byte, len(in))

	for i, b := range in {
		if b < 249 {
			out[i] = byte((int(b) * int(b) * int(b)) % 249) //nolint:gosec // mod 249 fits a byte
		} else {
			out[i] = b
		}
	}

	return out
}

func TestDecipherInvertsCipher(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}

	assert.Equal(t, all, Decipher(encipher(all)))
}

func TestDecipherKnownValues(t *testing.T) {
	// 2³ = 8, 3³ = 27, 7³ = 343 = 249 + 94; 0, 1 and 248 are fixed points.
	assert.Equal(t, []byte{2, 3, 7, 0, 1, 248, 250}, Decipher([]byte{8, 27, 94, 0, 1, 248, 250}))
}

// buildJPEG assembles the smallest JPEG that carries a Sony maker note:
// SOI, an APP1 EXIF segment holding IFD0 -> EXIF IFD -> maker note -> 0x9050.
func buildJPEG(t *testing.T, block []byte) []byte {
	t.Helper()

	le := binary.LittleEndian
	tiff := []byte("II*\x00\x08\x00\x00\x00")

	ifd := func(entries ...[]byte) []byte {
		out := le.AppendUint16(nil, uint16(len(entries))) //nolint:gosec // see above
		for _, e := range entries {
			out = append(out, e...)
		}

		return le.AppendUint32(out, 0)
	}
	ent := func(tag, typ uint16, count, value uint32) []byte {
		out := le.AppendUint16(nil, tag)
		out = le.AppendUint16(out, typ)
		out = le.AppendUint32(out, count)

		return le.AppendUint32(out, value)
	}

	// Every length below is a few hundred bytes at most, so the narrowing
	// conversions gosec flags cannot overflow.
	//
	// Layout: IFD0 at 8 (18 bytes), EXIF IFD at 26 (18), maker note at 44.
	makerAt := uint32(44)
	noteIFD := 18
	blockAt := makerAt + 12 + uint32(noteIFD)

	tiff = append(tiff, ifd(ent(tagExifIFD, 4, 1, 26))...)
	tiff = append(tiff, ifd(ent(tagMakerNote, 7, uint32(12+noteIFD+len(block)), makerAt))...) //nolint:gosec // see above
	tiff = append(tiff, []byte("SONY DSC \x00\x00\x00")...)
	tiff = append(tiff, ifd(ent(tag9050, 7, uint32(len(block)), blockAt))...) //nolint:gosec // see above
	tiff = append(tiff, block...)

	app1 := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xff, 0xd8, 0xff, 0xe1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(app1)+2)) //nolint:gosec // see above
	out = append(out, app1...)

	return append(out, 0xff, 0xd9)
}

func TestParseFrameCount(t *testing.T) {
	plain := make([]byte, 0x80)
	binary.LittleEndian.PutUint32(plain[0x3a:], 0x7f012345) // top byte is not part of the count
	plain[0x26] = 0x51

	frame, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050c)
	require.NoError(t, err)
	assert.Equal(t, uint32(0x012345), frame.ShutterCount)
	assert.Equal(t, ShutterMechanical, frame.Shutter)
	assert.Equal(t, plain, frame.Raw)
}

func TestParseFrameCountElectronic(t *testing.T) {
	plain := make([]byte, 0x80)
	plain[0x3a] = 9

	frame, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050c)
	require.NoError(t, err)
	assert.Equal(t, uint32(9), frame.ShutterCount)
	assert.Equal(t, ShutterElectronic, frame.Shutter)
}

func TestParseFrameCount9050d(t *testing.T) {
	plain := make([]byte, 0x80)
	binary.LittleEndian.PutUint32(plain[0x0a:], 0xaa001234)
	binary.LittleEndian.PutUint32(plain[0x3a:], 0x00999999) // the 9050c offset must be ignored

	frame, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050d)
	require.NoError(t, err)
	assert.Equal(t, uint32(0x1234), frame.ShutterCount)
	assert.Equal(t, ShutterUnknown, frame.Shutter)
}

func TestParseFrameCountShortBlock(t *testing.T) {
	_, err := ParseFrameCount(buildJPEG(t, encipher(make([]byte, 0x20))), Layout9050c)
	require.Error(t, err)
}

func TestParseFrameCountTruncatedHead(t *testing.T) {
	// A partial read can cut the APP1 segment short; the EXIF inside what
	// was fetched must still be found.
	plain := make([]byte, 0x80)
	plain[0x3a] = 42

	file := buildJPEG(t, encipher(plain))
	file = append(file[:len(file)-2], make([]byte, 64)...) // drop EOI, pad as if more followed
	binary.BigEndian.PutUint16(file[4:], 0xfff0)           // claim a longer segment than we hold

	frame, err := ParseFrameCount(file, Layout9050c)
	require.NoError(t, err)
	assert.Equal(t, uint32(42), frame.ShutterCount)
}

func TestParseFrameCountNoExif(t *testing.T) {
	_, err := ParseFrameCount([]byte{0xff, 0xd8, 0xff, 0xd9}, Layout9050c)
	require.ErrorIs(t, err, ErrNoExif)
}

func TestReadableDate(t *testing.T) {
	assert.Equal(t, "2026-11-09 22:32:40", readableDate("20261109T223240"))
	assert.Equal(t, "2026-11-09 22:32:40", readableDate("20261109T223240.0+0200"))
	assert.Empty(t, readableDate(""))
	assert.Equal(t, "yesterday", readableDate("yesterday"))
}

func TestLookupModel(t *testing.T) {
	assert.Equal(t, "Alpha 7 IV", lookupModel("ILCE-7M4", 0).Name)
	assert.Equal(t, "Alpha 7 IV", lookupModel("", 0x0da6).Name)
	assert.Equal(t, Layout9050d, lookupModel("ILCE-7CM2", 0).Layout)
	assert.Equal(t, Layout9050b, lookupModel("ILCE-7M3", 0).Layout)
	assert.Empty(t, lookupModel("ILCE-7M2", 0).ModelID, "a Tag9050a body must not match")
	assert.Empty(t, lookupModel("ILCE-7", 0).ModelID, "a Tag9050a body must not match")
	assert.Empty(t, lookupModel("ZV-E1", 0).ModelID, "no mechanical shutter, no count")
}

func TestParseFrameCountGuardedMechanical(t *testing.T) {
	plain := make([]byte, 0x80) // five leading zeros: a mechanical frame
	binary.LittleEndian.PutUint32(plain[0x0a:], 1234)

	frame, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050dMechanical)
	require.NoError(t, err)
	assert.Equal(t, uint32(1234), frame.ShutterCount)
	assert.Equal(t, ShutterMechanical, frame.Shutter)
}

func TestParseFrameCountGuardedElectronic(t *testing.T) {
	plain := make([]byte, 0x80)
	plain[2] = 0x11 // not the zero prefix: another layout
	binary.LittleEndian.PutUint32(plain[0x0a:], 1234)

	_, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050dMechanical)
	require.ErrorIs(t, err, ErrNoCountInFrame)

	// The unguarded layout would have read the same bytes as a count,
	// which is exactly the wrong number the guard is there to prevent.
	frame, err := ParseFrameCount(buildJPEG(t, encipher(plain)), Layout9050d)
	require.NoError(t, err)
	assert.Equal(t, uint32(1234), frame.ShutterCount)
}
