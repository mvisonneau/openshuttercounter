// Package manufacturers defines what openshuttercounter needs from a camera
// maker in order to report its actuation counters, so that support for a
// second one is a matter of adding a package rather than rearranging this one.
//
// Everything below the interface is shared and manufacturer agnostic: pkg/usb
// claims the interface, pkg/ptp carries the containers, pkg/trace records what
// was exchanged. A Reader supplies only the part that differs, which is how a
// given maker's bodies are asked for the count.
package manufacturers

import (
	"context"
	"fmt"
	"strings"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// CounterSpec describes a counter a model is known to expose.
type CounterSpec struct {
	// Label is what the CLI prints, for example "Shutter actuations".
	Label string `json:"label"`

	// Location is where the value lives, in whatever terms the
	// manufacturer's protocol uses. Canon bodies name an MPU address; a
	// different maker might name a property or a register.
	Location string `json:"location,omitempty"`
}

// Model is one camera body a Reader knows how to read.
type Model struct {
	// Name is the marketing name, for example "EOS 5D Mark IV".
	Name string `json:"name"`

	// ProductID is the USB product ID the body enumerates with.
	ProductID uint16 `json:"product_id"`

	// Confirmed records whether somebody with this body has checked the
	// readings against a known number of frames and reported back. An
	// unconfirmed entry is a reasonable guess, and the CLI says so.
	Confirmed bool `json:"confirmed"`

	// ConfirmedOn describes the firmware it was confirmed against.
	ConfirmedOn string `json:"confirmed_on,omitempty"`

	// RatedLife is the manufacturer's shutter durability rating, used only
	// to put a reading in context. Zero means unknown.
	RatedLife uint32 `json:"rated_life,omitempty"`

	// Counters is what this body is expected to report.
	Counters []CounterSpec `json:"counters,omitempty"`
}

// Counter is one counter, read.
type Counter struct {
	Label    string `json:"label"`
	Location string `json:"location,omitempty"`
	Value    uint32 `json:"value"`

	// Raw is the untouched reply, which is what somebody working out a new
	// body's layout actually needs to see.
	Raw []byte `json:"raw,omitempty"`

	// Err is set when this particular counter could not be read. One
	// counter failing does not invalidate the others.
	Err error `json:"-"`
}

// Reading is everything one camera reported in one go.
type Reading struct {
	Manufacturer string `json:"manufacturer"`

	// Model is the body's name if it was recognised, and a description of
	// what was found if it was not.
	Model string `json:"model"`

	// Confirmed carries the model's confirmation state through to the
	// output, so a reading from an unrecognised body is never presented as
	// though it were established.
	Confirmed bool `json:"confirmed"`

	// Method names how the values were obtained, for example the standard
	// property or a maker specific channel.
	Method string `json:"method"`

	RatedLife uint32 `json:"rated_life,omitempty"`

	Counters []Counter `json:"counters"`
}

// Reader reads actuation counters from one manufacturer's cameras.
type Reader interface {
	// Name is the manufacturer's name.
	Name() string

	// VendorID is the USB vendor ID its cameras enumerate with.
	VendorID() uint16

	// Models lists the bodies this reader knows about.
	Models() []Model

	// Read reports what the camera says. The PTP session is already open.
	Read(ctx context.Context, device *ptp.Device, info usb.DeviceInfo) (Reading, error)
}

// Registry is the set of readers a build knows about.
type Registry struct {
	readers []Reader
}

// NewRegistry builds a registry. Readers are consulted in the order given.
func NewRegistry(readers ...Reader) *Registry {
	return &Registry{readers: readers}
}

// Readers returns every registered reader.
func (r *Registry) Readers() []Reader {
	out := make([]Reader, len(r.readers))
	copy(out, r.readers)

	return out
}

// For returns the reader responsible for a device.
func (r *Registry) For(info usb.DeviceInfo) (Reader, error) {
	for _, reader := range r.readers {
		if reader.VendorID() == info.VendorID {
			return reader, nil
		}
	}

	return nil, fmt.Errorf(
		"no reader knows USB vendor %04x; openshuttercounter supports %s so far",
		info.VendorID, r.describe(),
	)
}

// VendorIDs returns the USB vendor IDs worth enumerating.
func (r *Registry) VendorIDs() []uint16 {
	out := make([]uint16, 0, len(r.readers))
	for _, reader := range r.readers {
		out = append(out, reader.VendorID())
	}

	return out
}

// describe lists the supported manufacturers for an error message.
func (r *Registry) describe() string {
	if len(r.readers) == 0 {
		return "no manufacturers"
	}

	names := make([]string, 0, len(r.readers))
	for _, reader := range r.readers {
		names = append(names, reader.Name())
	}

	return strings.Join(names, ", ")
}
