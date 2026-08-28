package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

func TestHumanInt(t *testing.T) {
	for value, want := range map[uint32]string{
		0:      "0",
		7:      "7",
		999:    "999",
		1000:   "1,000",
		26374:  "26,374",
		150000: "150,000",
	} {
		assert.Equal(t, want, humanInt(value))
	}
}

func TestMatchesSelector(t *testing.T) {
	device := usb.DeviceInfo{
		Path:      "/dev/bus/usb/001/007",
		Bus:       1,
		Address:   7,
		VendorID:  0x04a9,
		ProductID: 0x3281,
	}

	for _, selector := range []string{
		"/dev/bus/usb/001/007",
		"1:7",
		"001:007",
		"04a9:3281",
		"04A9:3281",
	} {
		assert.True(t, matchesSelector(device, selector), "selector %q should match", selector)
	}

	for _, selector := range []string{"2:7", "04a9:3282", "/dev/bus/usb/001/008", ""} {
		assert.False(t, matchesSelector(device, selector), "selector %q should not match", selector)
	}
}

func TestSpacedHex(t *testing.T) {
	assert.Equal(t, "00 66 00 00 f2 00", spacedHex("00660000f200"))
	assert.Empty(t, spacedHex(""))
}
