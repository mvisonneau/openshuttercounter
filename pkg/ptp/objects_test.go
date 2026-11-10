package ptp

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptpString(s string) []byte {
	if s == "" {
		return []byte{0}
	}

	// Test strings are short ASCII, so neither conversion can overflow.
	out := []byte{byte(len(s) + 1)} //nolint:gosec // see above
	for _, r := range s {
		out = binary.LittleEndian.AppendUint16(out, uint16(r)) //nolint:gosec // see above
	}

	return append(out, 0, 0)
}

func TestParseObjectInfo(t *testing.T) {
	le := binary.LittleEndian

	b := le.AppendUint32(nil, 0x10001)  // storage
	b = le.AppendUint16(b, 0x3801)      // format
	b = le.AppendUint16(b, 0)           // protection
	b = le.AppendUint32(b, 12345678)    // size
	b = le.AppendUint16(b, 0x3808)      // thumb format
	b = append(b, make([]byte, 6*4)...) // thumb size, dims, depth
	b = le.AppendUint32(b, 0x42)        // parent
	b = le.AppendUint16(b, 0)           // association type
	b = append(b, make([]byte, 8)...)   // association desc, sequence
	b = append(b, ptpString("DSC01234.JPG")...)
	b = append(b, ptpString("20261110T104500")...)
	b = append(b, ptpString("")...)
	b = append(b, ptpString("")...)

	info, err := ParseObjectInfo(b)
	require.NoError(t, err)
	assert.Equal(t, ObjectInfo{
		StorageID:      0x10001,
		Format:         0x3801,
		CompressedSize: 12345678,
		ParentObject:   0x42,
		Filename:       "DSC01234.JPG",
		CaptureDate:    "20261110T104500",
	}, info)
}

func TestParseObjectInfoTruncated(t *testing.T) {
	_, err := ParseObjectInfo([]byte{1, 2, 3})
	require.ErrorIs(t, err, ErrShortData)
}
