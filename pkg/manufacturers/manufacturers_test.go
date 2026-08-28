package manufacturers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

type fakeReader struct {
	name     string
	vendorID uint16
}

func (f fakeReader) Name() string     { return f.name }
func (f fakeReader) VendorID() uint16 { return f.vendorID }
func (f fakeReader) Models() []Model  { return nil }

func (f fakeReader) Read(_ context.Context, _ *ptp.Device, _ usb.DeviceInfo) (Reading, error) {
	return Reading{Manufacturer: f.name}, nil
}

func TestRegistryRoutesByVendor(t *testing.T) {
	canon := fakeReader{name: "Canon", vendorID: 0x04a9}
	nikon := fakeReader{name: "Nikon", vendorID: 0x04b0}

	reg := NewRegistry(canon, nikon)

	assert.Equal(t, []uint16{0x04a9, 0x04b0}, reg.VendorIDs())
	assert.Len(t, reg.Readers(), 2)

	reader, err := reg.For(usb.DeviceInfo{VendorID: 0x04b0})
	require.NoError(t, err)
	assert.Equal(t, "Nikon", reader.Name())
}

// TestRegistryExplainsWhatItSupports matters because an unknown vendor is the
// single most likely thing a new user hits, and "no reader" on its own tells
// them nothing about what to do next.
func TestRegistryExplainsWhatItSupports(t *testing.T) {
	reg := NewRegistry(fakeReader{name: "Canon", vendorID: 0x04a9})

	_, err := reg.For(usb.DeviceInfo{VendorID: 0x1234})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1234")
	assert.Contains(t, err.Error(), "Canon")
}

func TestEmptyRegistry(t *testing.T) {
	reg := NewRegistry()

	assert.Empty(t, reg.VendorIDs())

	_, err := reg.For(usb.DeviceInfo{VendorID: 0x04a9})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no manufacturers")
}
