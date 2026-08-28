package usb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eos5DMarkIVConfig is the configuration descriptor of an EOS 5D Mark IV: one
// still image interface with a bulk pair and an interrupt endpoint.
var eos5DMarkIVConfig = []byte{
	// configuration descriptor
	0x09, 0x02, 0x27, 0x00, 0x01, 0x01, 0x00, 0xc0, 0x01,
	// interface 0, class 6 (still image), 3 endpoints
	0x09, 0x04, 0x00, 0x00, 0x03, 0x06, 0x01, 0x01, 0x00,
	// endpoint 0x81, bulk IN, 512 bytes
	0x07, 0x05, 0x81, 0x02, 0x00, 0x02, 0x00,
	// endpoint 0x02, bulk OUT, 512 bytes
	0x07, 0x05, 0x02, 0x02, 0x00, 0x02, 0x00,
	// endpoint 0x83, interrupt IN, 8 bytes
	0x07, 0x05, 0x83, 0x03, 0x08, 0x00, 0x08,
}

func TestParseConfigFindsThePTPEndpoints(t *testing.T) {
	cfg, err := ParseConfig(eos5DMarkIVConfig)
	require.NoError(t, err)
	require.Len(t, cfg.Interfaces, 1)

	iface, ok := cfg.StillImageInterface()
	require.True(t, ok)
	assert.Equal(t, uint8(0), iface.Number)
	assert.Equal(t, ClassStillImage, iface.Class)
	assert.Len(t, iface.Endpoints, 3)

	in, ok := iface.BulkIn()
	require.True(t, ok)
	assert.Equal(t, uint8(0x81), in.Address)
	assert.Equal(t, uint16(512), in.MaxPacketSize)

	out, ok := iface.BulkOut()
	require.True(t, ok)
	assert.Equal(t, uint8(0x02), out.Address)

	// The interrupt endpoint must not be mistaken for the bulk one.
	assert.NotEqual(t, uint8(0x83), in.Address)
}

// TestParseConfigSkipsNonPTPInterfaces covers bodies that also expose a mass
// storage or vendor specific interface, where claiming interface zero blindly
// would claim the wrong one.
func TestParseConfigSkipsNonPTPInterfaces(t *testing.T) {
	desc := []byte{
		0x09, 0x02, 0x37, 0x00, 0x02, 0x01, 0x00, 0xc0, 0x01,
		// interface 0, mass storage
		0x09, 0x04, 0x00, 0x00, 0x02, 0x08, 0x06, 0x50, 0x00,
		0x07, 0x05, 0x81, 0x02, 0x00, 0x02, 0x00,
		0x07, 0x05, 0x02, 0x02, 0x00, 0x02, 0x00,
		// interface 1, still image
		0x09, 0x04, 0x01, 0x00, 0x02, 0x06, 0x01, 0x01, 0x00,
		0x07, 0x05, 0x84, 0x02, 0x00, 0x02, 0x00,
		0x07, 0x05, 0x05, 0x02, 0x00, 0x02, 0x00,
	}

	cfg, err := ParseConfig(desc)
	require.NoError(t, err)
	require.Len(t, cfg.Interfaces, 2)

	iface, ok := cfg.PTPInterface()
	require.True(t, ok)
	assert.Equal(t, uint8(1), iface.Number)

	in, _ := iface.BulkIn()
	out, _ := iface.BulkOut()
	assert.Equal(t, uint8(0x84), in.Address)
	assert.Equal(t, uint8(0x05), out.Address)
}

func TestParseConfigSkipsUnknownDescriptors(t *testing.T) {
	desc := []byte{
		0x09, 0x02, 0x28, 0x00, 0x01, 0x01, 0x00, 0xc0, 0x01,
		// an interface association descriptor we do not care about
		0x08, 0x0b, 0x00, 0x01, 0x06, 0x01, 0x01, 0x00,
		0x09, 0x04, 0x00, 0x00, 0x02, 0x06, 0x01, 0x01, 0x00,
		0x07, 0x05, 0x81, 0x02, 0x00, 0x02, 0x00,
		0x07, 0x05, 0x02, 0x02, 0x00, 0x02, 0x00,
	}

	cfg, err := ParseConfig(desc)
	require.NoError(t, err)
	require.Len(t, cfg.Interfaces, 1)
	assert.Len(t, cfg.Interfaces[0].Endpoints, 2)
}

func TestParseConfigRejectsRubbish(t *testing.T) {
	_, err := ParseConfig([]byte{0x09, 0x01, 0x00})
	require.Error(t, err)

	_, err = ParseConfig(nil)
	require.Error(t, err)
}

func TestParseDeviceDescriptor(t *testing.T) {
	// An EOS 5D Mark IV: 04a9:3281.
	desc := []byte{
		0x12, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x40,
		0xa9, 0x04, 0x81, 0x32, 0x00, 0x01, 0x01, 0x02, 0x03, 0x01,
	}

	got, err := ParseDeviceDescriptor(desc)
	require.NoError(t, err)
	assert.Equal(t, uint16(0x04a9), got.VendorID)
	assert.Equal(t, uint16(0x3281), got.ProductID)
	assert.Equal(t, uint8(1), got.ManufacturerIndex)
	assert.Equal(t, uint8(2), got.ProductIndex)
	assert.Equal(t, uint8(3), got.SerialIndex)

	_, err = ParseDeviceDescriptor(desc[:10])
	require.Error(t, err)
}

func TestDecodeStringDescriptor(t *testing.T) {
	// "Canon" as a USB string descriptor.
	desc := []byte{0x0c, 0x03, 'C', 0, 'a', 0, 'n', 0, 'o', 0, 'n', 0}
	assert.Equal(t, "Canon", decodeStringDescriptor(desc))

	assert.Empty(t, decodeStringDescriptor(nil))
	assert.Empty(t, decodeStringDescriptor([]byte{0x04, 0x01, 0x00, 0x00}))
}
