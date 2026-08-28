package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"
	"go.uber.org/zap"

	"github.com/mvisonneau/openshuttercounter/internal/app"
	"github.com/mvisonneau/openshuttercounter/internal/logs"
	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers"
	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers/canon"
	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/trace"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// registry lists the manufacturers this build can read.
//
// Adding another one is a matter of writing a package under
// pkg/manufacturers/ and naming it here; nothing else in the CLI is
// manufacturer aware.
func registry(logger *zap.Logger, factoryOnly bool) *manufacturers.Registry {
	return manufacturers.NewRegistry(
		canon.New(canon.WithLogger(logger), canon.WithFactoryOnly(factoryOnly)),
	)
}

func configure(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	logger, encoding, err := logs.NewLogger(cmd.String("log-level"), cmd.String("log-format"))
	if err != nil {
		return ctx, err
	}

	return logs.StoreLoggerInContext(ctx, logger, encoding), nil
}

// Execute configures logging and then runs the command.
func Execute(f func(ctx context.Context, cmd *cli.Command) error) func(context.Context, *cli.Command) error {
	return func(ctx context.Context, cmd *cli.Command) error {
		var err error

		if ctx, err = configure(ctx, cmd); err != nil {
			return err
		}

		return f(ctx, cmd)
	}
}

// selectDevice finds the camera to talk to.
//
// With no --device the choice has to be unambiguous: guessing between two
// connected bodies would mean reporting one camera's shutter count under
// another's name.
func selectDevice(reg *manufacturers.Registry, selector string) (usb.DeviceInfo, error) {
	devices, err := usb.ListVendors(reg.VendorIDs()...)
	if err != nil {
		return usb.DeviceInfo{}, fmt.Errorf("enumerating USB devices: %w", err)
	}

	if selector != "" {
		for _, d := range devices {
			if matchesSelector(d, selector) {
				return d, nil
			}
		}

		return usb.DeviceInfo{}, fmt.Errorf("no connected camera matches %q", selector)
	}

	switch len(devices) {
	case 0:
		return usb.DeviceInfo{}, noDeviceError{}
	case 1:
		return devices[0], nil
	default:
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = d.Path
		}

		return usb.DeviceInfo{}, fmt.Errorf(
			"%d cameras are connected; pick one with --device: %s",
			len(devices), strings.Join(names, ", "),
		)
	}
}

// matchesSelector accepts a device path, a bus:address pair or a product ID.
func matchesSelector(d usb.DeviceInfo, selector string) bool {
	switch {
	case d.Path == selector:
		return true
	case fmt.Sprintf("%d:%d", d.Bus, d.Address) == selector:
		return true
	case fmt.Sprintf("%03d:%03d", d.Bus, d.Address) == selector:
		return true
	case fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID) == strings.ToLower(selector):
		return true
	default:
		return false
	}
}

// noDeviceError carries the guidance shown when nothing is plugged in. It is a
// type rather than errors.New because the text is several sentences of help,
// which is exactly what a one line error message is not meant to be.
type noDeviceError struct{}

func (noDeviceError) Error() string { return noDeviceHint }

const noDeviceHint = `no camera found. Check that:
  - the camera is switched on and connected directly, not through a hub
  - the cable is a data cable rather than charge only
  - built-in Wi-Fi is fully disabled: on many bodies it disables the USB port
  - there is a card in the camera
  - USB 2 ports are noticeably more reliable than USB 3 with these bodies

If your camera is from a manufacturer this build does not know yet, it will
not be listed at all. Adding one is welcome; see CONTRIBUTING.md.`

// openDevice claims the camera, working around whatever is holding it.
func openDevice(ctx context.Context, info usb.DeviceInfo) (usb.Device, error) {
	logger := logs.LoggerFromContext(ctx)

	device, err := usb.Open(info)
	if err == nil {
		return device, nil
	}

	if !errors.Is(err, usb.ErrBusy) {
		return nil, err
	}

	// macOS hands every camera to ptpcamerad the moment it appears. Stop it
	// and try once more; launchd brings it back when we are done.
	if runtime.GOOS == "darwin" {
		logger.Warn("the camera is held by another process, stopping ptpcamerad and retrying")

		if err := usb.ReleaseSystemCameraDaemon(ctx); err != nil {
			logger.Debug("could not stop ptpcamerad", zap.Error(err))
		}

		if device, retryErr := usb.Open(info); retryErr == nil {
			return device, nil
		}
	}

	return nil, fmt.Errorf("%w\n%s", err, busyHint())
}

func busyHint() string {
	switch runtime.GOOS {
	case "linux":
		return `On desktop Linux gvfs claims cameras on sight. Release it with:
  gio mount -u gphoto2://
  systemctl --user mask gvfs-gphoto2-volume-monitor`
	case "darwin":
		return `macOS gives cameras to ptpcamerad. Release it with:
  killall ptpcamerad`
	case "windows":
		return "Close any other software that may have opened the camera."
	default:
		return ""
	}
}

// session is an open conversation with a camera, plus the reader that knows
// how to ask it for a count.
type session struct {
	device   *ptp.Device
	info     usb.DeviceInfo
	reader   manufacturers.Reader
	recorder *trace.Recorder

	tracePath string
	close     func()
}

// openSession picks a camera, claims it and starts a PTP session.
//
// When --trace is given, every container exchanged from this point on is
// recorded, including the ones sent while working out what the camera is.
func openSession(ctx context.Context, cmd *cli.Command) (*session, error) {
	logger := logs.LoggerFromContext(ctx)
	reg := registry(logger, cmd.Bool("factory-only"))

	info, err := selectDevice(reg, cmd.String("device"))
	if err != nil {
		return nil, err
	}

	reader, err := reg.For(info)
	if err != nil {
		return nil, err
	}

	logger.Debug("opening device",
		zap.String("path", info.Path),
		zap.String("device", info.String()),
		zap.String("manufacturer", reader.Name()),
	)

	transport, err := openDevice(ctx, info)
	if err != nil {
		return nil, err
	}

	opts := []ptp.Option{ptp.WithLogger(logger)}

	s := &session{info: info, reader: reader, tracePath: cmd.String("trace")}

	if s.tracePath != "" {
		s.recorder = trace.NewRecorder(app.Version)
		s.recorder.Annotate(cmd.String("note"))
		opts = append(opts, ptp.WithRecorder(s.recorder))
	}

	s.device = ptp.New(transport, opts...)

	s.close = func() {
		if err := s.device.CloseSession(); err != nil {
			logger.Debug("closing the PTP session", zap.Error(err))
		}

		if err := transport.Close(); err != nil {
			logger.Debug("closing the USB device", zap.Error(err))
		}
	}

	if err := s.device.OpenSession(); err != nil {
		s.close()

		return nil, err
	}

	// The identity is only fully known once the interface is open, since
	// some backends read the descriptors on the way in.
	s.info = s.device.Info()
	if s.recorder != nil {
		s.recorder.Describe(s.info)
	}

	return s, nil
}

// saveTrace writes the recording out, if one was asked for.
func (s *session) saveTrace(ctx context.Context) error {
	if s.recorder == nil {
		return nil
	}

	if err := s.recorder.WriteFile(s.tracePath); err != nil {
		return err
	}

	logs.LoggerFromContext(ctx).Info("trace written",
		zap.String("path", s.tracePath),
		zap.Int("entries", len(s.recorder.File().Entries)),
	)

	return nil
}

// humanInt groups a number in threes, so a shutter count reads as 26,374.
func humanInt(v uint32) string {
	digits := strconv.FormatUint(uint64(v), 10)

	var out strings.Builder

	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}

		out.WriteRune(r)
	}

	return out.String()
}
