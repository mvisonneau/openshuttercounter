package canon

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers"
	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// How a reading was obtained, reported so that anyone comparing two readings
// knows whether they came from the same place.
const (
	// MethodStandardProperty is the standard PTP shutter counter property,
	// which bodies up to roughly the DIGIC 5 generation still answer.
	MethodStandardProperty = "ptp-property-0xd1ac"

	// MethodFactoryChannel is the maker specific channel that reaches the
	// MPU's own memory, which is the only route on DIGIC 6 and newer.
	MethodFactoryChannel = "canon-factory-channel"
)

// Reader reads actuation counters from Canon EOS cameras.
type Reader struct {
	logger *zap.Logger

	// factoryOnly skips the standard property and goes straight to the
	// factory channel. Useful when comparing the two on a body that
	// answers both.
	factoryOnly bool
}

// Option configures a Reader.
type Option func(*Reader)

// WithLogger attaches a logger.
func WithLogger(l *zap.Logger) Option {
	return func(r *Reader) {
		if l != nil {
			r.logger = l
		}
	}
}

// WithFactoryOnly skips the standard property probe.
func WithFactoryOnly(only bool) Option {
	return func(r *Reader) { r.factoryOnly = only }
}

// New builds a Canon reader.
func New(opts ...Option) *Reader {
	r := &Reader{logger: zap.NewNop()}

	for _, o := range opts {
		o(r)
	}

	return r
}

// Name implements manufacturers.Reader.
func (r *Reader) Name() string { return "Canon" }

// VendorID implements manufacturers.Reader.
func (r *Reader) VendorID() uint16 { return VendorID }

// Models implements manufacturers.Reader.
func (r *Reader) Models() []manufacturers.Model {
	known := Models()
	out := make([]manufacturers.Model, 0, len(known))

	for _, m := range known {
		counters := make([]manufacturers.CounterSpec, 0, len(m.Counters))
		for _, c := range m.Counters {
			counters = append(counters, manufacturers.CounterSpec{
				Label:    c.Label,
				Location: fmt.Sprintf("MPU %#04x", c.Address),
			})
		}

		out = append(out, manufacturers.Model{
			Name:        m.Name,
			ProductID:   m.ProductID,
			Confirmed:   m.Confirmed,
			ConfirmedOn: m.ConfirmedOn,
			RatedLife:   m.RatedLife,
			Counters:    counters,
		})
	}

	return out
}

// Read implements manufacturers.Reader.
//
// The standard property is tried first. When a body answers it there is no
// reason to go anywhere near the factory channel, and every camera older than
// roughly DIGIC 6 does answer it.
func (r *Reader) Read(_ context.Context, device *ptp.Device, info usb.DeviceInfo) (manufacturers.Reading, error) {
	if !r.factoryOnly {
		if reading, ok := r.readStandardProperty(device, info); ok {
			return reading, nil
		}
	}

	return r.readFactoryChannel(device, info)
}

// readStandardProperty tries PTP property 0xD1AC. The boolean reports whether
// the camera carries it at all; a body that does not is the expected case here
// and is not an error.
func (r *Reader) readStandardProperty(device *ptp.Device, info usb.DeviceInfo) (manufacturers.Reading, bool) {
	count, err := LegacyShutterCounter(device)

	switch {
	case err == nil:
	case errors.Is(err, ErrLegacyCounterUnsupported):
		r.logger.Debug("the standard shutter counter property is absent, as expected on DIGIC 6 and newer")

		return manufacturers.Reading{}, false
	default:
		// Anything other than a clean "not supported" is still worth
		// noting, but it leaves the factory channel worth trying.
		r.logger.Debug("the standard shutter counter probe failed", zap.Error(err))

		return manufacturers.Reading{}, false
	}

	return manufacturers.Reading{
		Manufacturer: r.Name(),
		Model:        modelName(info),
		Confirmed:    true,
		Method:       MethodStandardProperty,
		Counters: []manufacturers.Counter{
			{Label: "Shutter actuations", Value: count},
		},
	}, true
}

// readFactoryChannel reads the counters out of MPU memory.
func (r *Reader) readFactoryChannel(device *ptp.Device, info usb.DeviceInfo) (manufacturers.Reading, error) {
	model, known := LookupModel(info.ProductID)
	if !known {
		model = FallbackModel(info.ProductID)

		r.logger.Warn("this body is not in the list of confirmed cameras, so the values below are a guess",
			zap.String("product_id", fmt.Sprintf("%04x", info.ProductID)),
			zap.String("what_would_help", "run with --trace and open an issue; see CONTRIBUTING.md"),
		)
	}

	name := model.Name
	if known && info.Product != "" {
		name = info.Product
	}

	readings, err := ReadModel(device, model)
	if err != nil {
		return manufacturers.Reading{}, err
	}

	out := manufacturers.Reading{
		Manufacturer: r.Name(),
		Model:        name,
		Confirmed:    model.Confirmed,
		Method:       MethodFactoryChannel,
		RatedLife:    model.RatedLife,
		Counters:     make([]manufacturers.Counter, 0, len(readings)),
	}

	for _, reading := range readings {
		out.Counters = append(out.Counters, manufacturers.Counter{
			Label:    reading.Counter.Label,
			Location: fmt.Sprintf("MPU %#04x", reading.Counter.Address),
			Value:    reading.Value,
			Raw:      reading.Raw,
			Err:      reading.Err,
		})
	}

	return out, nil
}

// modelName prefers what the camera calls itself over anything we assume.
func modelName(info usb.DeviceInfo) string {
	if info.Product != "" {
		return info.Product
	}

	return "Canon camera"
}
