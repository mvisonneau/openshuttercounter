//go:build darwin

package usb

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The macOS backend drives libusb-1.0, loaded at runtime with purego rather
// than linked with cgo. That keeps CGO_ENABLED=0 across the whole project, so
// the release pipeline can still cross compile every target from one machine,
// and it keeps the binary free of any build time dependency.
//
// libusb is the practical choice here because IOKit's USB device interfaces
// are CoreFoundation plug-ins, which are considerably more work to drive
// through a runtime FFI than a flat C API. Going through ImageCaptureCore
// instead, which would remove the dependency altogether, is noted as future
// work in the README.

// libusbCandidates are searched in order. The first entry lets dyld apply its
// own search rules; the rest cover Homebrew on Apple silicon, Homebrew on
// Intel, and MacPorts.
var libusbCandidates = []string{
	"libusb-1.0.0.dylib",
	"/opt/homebrew/lib/libusb-1.0.0.dylib",
	"/usr/local/lib/libusb-1.0.0.dylib",
	"/opt/local/lib/libusb-1.0.0.dylib",
	"libusb-1.0.dylib",
}

// libusb error codes we treat specially.
const (
	libusbSuccess      int32 = 0
	libusbErrorAccess  int32 = -3
	libusbErrorBusy    int32 = -6
	libusbErrorTimeout int32 = -7
)

// libusbDeviceDescriptor mirrors struct libusb_device_descriptor, which has
// the same layout as the on the wire descriptor: naturally aligned, 18 bytes.
type libusbDeviceDescriptor struct {
	Length            uint8
	DescriptorType    uint8
	USBVersion        uint16
	DeviceClass       uint8
	DeviceSubClass    uint8
	DeviceProtocol    uint8
	MaxPacketSize0    uint8
	VendorID          uint16
	ProductID         uint16
	DeviceVersion     uint16
	ManufacturerIndex uint8
	ProductIndex      uint8
	SerialIndex       uint8
	NumConfigurations uint8
}

type libusbAPI struct {
	initialize func(ctx *uintptr) int32
	exit       func(ctx uintptr)

	getDeviceList    func(ctx uintptr, list **uintptr) int64
	freeDeviceList   func(list *uintptr, unref int32)
	getDescriptor    func(dev uintptr, desc unsafe.Pointer) int32
	getBusNumber     func(dev uintptr) uint8
	getDeviceAddress func(dev uintptr) uint8
	openDevice       func(dev uintptr, handle *uintptr) int32
	closeDevice      func(handle uintptr)
	claimInterface   func(handle uintptr, iface int32) int32
	releaseInterface func(handle uintptr, iface int32) int32
	bulkTransfer     func(handle uintptr, endpoint uint8, data unsafe.Pointer, length int32,
		transferred *int32, timeout uint32) int32
	controlTransfer func(handle uintptr, requestType uint8, request uint8, value uint16,
		index uint16, data unsafe.Pointer, length uint16, timeout uint32) int32
	clearHalt        func(handle uintptr, endpoint uint8) int32
	stringDescriptor func(handle uintptr, index uint8, data unsafe.Pointer, length int32) int32
	autoDetachDriver func(handle uintptr, enable int32) int32
	errorName        func(code int32) string
}

var (
	libusbOnce sync.Once
	libusbLib  *libusbAPI
	libusbCtx  uintptr
	errLibusb  error
)

// loadLibusb dlopens libusb and binds the entry points we use.
func loadLibusb() (*libusbAPI, uintptr, error) {
	libusbOnce.Do(func() {
		var handle uintptr

		var err error

		for _, candidate := range libusbCandidates {
			if handle, err = purego.Dlopen(candidate, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
				break
			}
		}

		if handle == 0 {
			errLibusb = fmt.Errorf(
				"libusb-1.0 could not be loaded (%w); install it with: brew install libusb", err,
			)

			return
		}

		api := &libusbAPI{}

		defer func() {
			// RegisterLibFunc panics on a missing symbol, which would mean
			// an unexpectedly old libusb rather than a programming error.
			if r := recover(); r != nil {
				errLibusb = fmt.Errorf("binding libusb symbols: %v", r)
				libusbLib = nil
			}
		}()

		purego.RegisterLibFunc(&api.initialize, handle, "libusb_init")
		purego.RegisterLibFunc(&api.exit, handle, "libusb_exit")
		purego.RegisterLibFunc(&api.getDeviceList, handle, "libusb_get_device_list")
		purego.RegisterLibFunc(&api.freeDeviceList, handle, "libusb_free_device_list")
		purego.RegisterLibFunc(&api.getDescriptor, handle, "libusb_get_device_descriptor")
		purego.RegisterLibFunc(&api.getBusNumber, handle, "libusb_get_bus_number")
		purego.RegisterLibFunc(&api.getDeviceAddress, handle, "libusb_get_device_address")
		purego.RegisterLibFunc(&api.openDevice, handle, "libusb_open")
		purego.RegisterLibFunc(&api.closeDevice, handle, "libusb_close")
		purego.RegisterLibFunc(&api.claimInterface, handle, "libusb_claim_interface")
		purego.RegisterLibFunc(&api.releaseInterface, handle, "libusb_release_interface")
		purego.RegisterLibFunc(&api.bulkTransfer, handle, "libusb_bulk_transfer")
		purego.RegisterLibFunc(&api.controlTransfer, handle, "libusb_control_transfer")
		purego.RegisterLibFunc(&api.clearHalt, handle, "libusb_clear_halt")
		purego.RegisterLibFunc(&api.stringDescriptor, handle, "libusb_get_string_descriptor_ascii")
		purego.RegisterLibFunc(&api.autoDetachDriver, handle, "libusb_set_auto_detach_kernel_driver")
		purego.RegisterLibFunc(&api.errorName, handle, "libusb_error_name")

		var ctx uintptr
		if rc := api.initialize(&ctx); rc != libusbSuccess {
			errLibusb = fmt.Errorf("libusb_init: %s", api.errorName(rc))

			return
		}

		libusbLib, libusbCtx = api, ctx
	})

	return libusbLib, libusbCtx, errLibusb
}

func (a *libusbAPI) err(op string, rc int32) error {
	switch rc {
	case libusbErrorBusy, libusbErrorAccess:
		return fmt.Errorf("%s: %s: %w", op, a.errorName(rc), ErrBusy)
	default:
		return fmt.Errorf("%s: %s (%d)", op, a.errorName(rc), rc)
	}
}

// list enumerates every attached USB device.
//
// Unlike the Linux backend this does not filter on the still image class:
// determining it would mean opening each device, which is intrusive and fails
// on anything another process holds. Callers that want cameras should use
// ListVendor; Open verifies the interface exists before claiming it.
func list() ([]DeviceInfo, error) {
	api, ctx, err := loadLibusb()
	if err != nil {
		return nil, err
	}

	handles, release, err := deviceList(api, ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	devices := make([]DeviceInfo, 0, len(handles))

	for _, dev := range handles {
		if dev == 0 {
			continue
		}

		var desc libusbDeviceDescriptor
		if rc := api.getDescriptor(dev, unsafe.Pointer(&desc)); rc != libusbSuccess {
			continue
		}

		bus := api.getBusNumber(dev)
		address := api.getDeviceAddress(dev)

		devices = append(devices, DeviceInfo{
			Path:      fmt.Sprintf("usb:%03d:%03d", bus, address),
			Bus:       uint16(bus),
			Address:   uint16(address),
			VendorID:  desc.VendorID,
			ProductID: desc.ProductID,
		})
	}

	return devices, nil
}

// deviceList returns libusb's device array as a Go slice, together with the
// function that releases it. The array is C owned, so it is only valid until
// release is called.
func deviceList(api *libusbAPI, ctx uintptr) ([]uintptr, func(), error) {
	var head *uintptr

	count := api.getDeviceList(ctx, &head)
	if count < 0 {
		return nil, func() {}, api.err("libusb_get_device_list", clampInt32(count))
	}

	if head == nil || count == 0 {
		return nil, func() { api.freeDeviceList(head, 1) }, nil
	}

	return unsafe.Slice(head, count), func() { api.freeDeviceList(head, 1) }, nil
}

// darwinDevice is an opened libusb handle with a claimed interface.
type darwinDevice struct {
	api    *libusbAPI
	handle uintptr
	info   DeviceInfo
	iface  int32
	epIn   uint8
	epOut  uint8
}

func open(info DeviceInfo) (Device, error) {
	api, ctx, err := loadLibusb()
	if err != nil {
		return nil, err
	}

	handles, release, err := deviceList(api, ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	var handle uintptr

	for _, dev := range handles {
		if dev == 0 {
			continue
		}

		if uint16(api.getBusNumber(dev)) != info.Bus || uint16(api.getDeviceAddress(dev)) != info.Address {
			continue
		}

		if rc := api.openDevice(dev, &handle); rc != libusbSuccess {
			return nil, fmt.Errorf(
				"%w (macOS hands cameras to ptpcamerad; stop it with: killall ptpcamerad)",
				api.err("libusb_open", rc),
			)
		}

		break
	}

	if handle == 0 {
		return nil, fmt.Errorf("%s: %w", info.Path, ErrNoDevice)
	}

	d := &darwinDevice{api: api, handle: handle, info: info}

	if err := d.configure(); err != nil {
		api.closeDevice(handle)

		return nil, err
	}

	return d, nil
}

func (d *darwinDevice) Info() DeviceInfo { return d.info }

func (d *darwinDevice) Read(p []byte, timeout time.Duration) (int, error) {
	return d.bulk(d.epIn, p, timeout)
}

func (d *darwinDevice) Write(p []byte, timeout time.Duration) (int, error) {
	return d.bulk(d.epOut, p, timeout)
}

func (d *darwinDevice) ClearHalt() error {
	for _, ep := range []uint8{d.epIn, d.epOut} {
		if rc := d.api.clearHalt(d.handle, ep); rc != libusbSuccess {
			return d.api.err(fmt.Sprintf("libusb_clear_halt (endpoint %#02x)", ep), rc)
		}
	}

	return nil
}

func (d *darwinDevice) Close() error {
	d.api.releaseInterface(d.handle, d.iface)
	d.api.closeDevice(d.handle)

	return nil
}

// configure reads the configuration descriptor over the control endpoint,
// picks the PTP interface and claims it.
//
// Fetching the raw descriptor rather than walking libusb's parsed structs
// means the same parser is used on every platform, and it is the parser that
// carries the unit tests.
func (d *darwinDevice) configure() error {
	raw, err := d.configDescriptor()
	if err != nil {
		return err
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("parsing configuration descriptor: %w", err)
	}

	iface, ok := cfg.PTPInterface()
	if !ok {
		return fmt.Errorf("%s exposes no still image interface", d.info.Path)
	}

	epIn, okIn := iface.BulkIn()
	epOut, okOut := iface.BulkOut()

	if !okIn || !okOut {
		return fmt.Errorf("interface %d has no bulk endpoint pair", iface.Number)
	}

	d.iface, d.epIn, d.epOut = int32(iface.Number), epIn.Address, epOut.Address

	d.readIdentity()

	// Ask libusb to detach whatever holds the interface where the platform
	// supports it. It is a no-op on macOS but harmless, and it keeps the
	// code honest if this backend is ever reused elsewhere.
	_ = d.api.autoDetachDriver(d.handle, 1)

	if rc := d.api.claimInterface(d.handle, d.iface); rc != libusbSuccess {
		return fmt.Errorf(
			"%w (macOS hands cameras to ptpcamerad; stop it with: killall ptpcamerad)",
			d.api.err("libusb_claim_interface", rc),
		)
	}

	return nil
}

// configDescriptor fetches configuration descriptor zero: nine bytes first to
// learn wTotalLength, then the whole thing.
func (d *darwinDevice) configDescriptor() ([]byte, error) {
	header, err := d.descriptor(descriptorTypeConfig, 9)
	if err != nil {
		return nil, err
	}

	total := int(header[2]) | int(header[3])<<8
	if total < len(header) {
		return nil, fmt.Errorf("configuration descriptor claims %d bytes", total)
	}

	return d.descriptor(descriptorTypeConfig, total)
}

// descriptor performs a standard GET_DESCRIPTOR control transfer.
func (d *darwinDevice) descriptor(descriptorType uint8, length int) ([]byte, error) {
	if length <= 0 || length > math.MaxUint16 {
		return nil, fmt.Errorf("descriptor length %d is out of range", length)
	}

	const (
		requestTypeStandardIn uint8  = 0x80
		requestGetDescriptor  uint8  = 0x06
		controlTimeoutMS      uint32 = 1000
	)

	buf := make([]byte, length)
	value := uint16(descriptorType) << 8

	rc := d.api.controlTransfer(d.handle, requestTypeStandardIn, requestGetDescriptor,
		value, 0, unsafe.Pointer(&buf[0]), uint16(length), controlTimeoutMS)
	if rc < 0 {
		return nil, d.api.err(fmt.Sprintf("libusb_control_transfer (descriptor %#02x)", descriptorType), rc)
	}

	if int(rc) < length {
		return nil, fmt.Errorf("descriptor %#02x is %d bytes, expected %d", descriptorType, rc, length)
	}

	return buf[:rc], nil
}

// readIdentity fills in the human readable strings. They are cosmetic, so a
// device that refuses to hand them over is not an error.
func (d *darwinDevice) readIdentity() {
	raw, err := d.descriptor(descriptorTypeDevice, 18)
	if err != nil {
		return
	}

	desc, err := ParseDeviceDescriptor(raw)
	if err != nil {
		return
	}

	d.info.Manufacturer = d.stringDescriptor(desc.ManufacturerIndex)
	d.info.Product = d.stringDescriptor(desc.ProductIndex)
	d.info.Serial = d.stringDescriptor(desc.SerialIndex)
}

// stringDescriptor reads a string descriptor, returning "" when absent.
func (d *darwinDevice) stringDescriptor(index uint8) string {
	if index == 0 {
		return ""
	}

	const stringDescriptorMax = 255

	buf := make([]byte, stringDescriptorMax)

	rc := d.api.stringDescriptor(d.handle, index, unsafe.Pointer(&buf[0]), stringDescriptorMax)
	if rc <= 0 {
		return ""
	}

	return string(buf[:rc])
}

func (d *darwinDevice) bulk(endpoint uint8, p []byte, timeout time.Duration) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	var transferred int32

	rc := d.api.bulkTransfer(d.handle, endpoint, unsafe.Pointer(&p[0]), clampInt32(int64(len(p))),
		&transferred, clampUint32(timeout.Milliseconds()))

	// A timeout that still moved bytes is a completed short transfer as far
	// as PTP is concerned, so it is only an error when nothing arrived.
	if rc == libusbErrorTimeout && transferred > 0 {
		return int(transferred), nil
	}

	if rc != libusbSuccess {
		return int(transferred), d.api.err(fmt.Sprintf("libusb_bulk_transfer (endpoint %#02x)", endpoint), rc)
	}

	return int(transferred), nil
}

// releaseSystemCameraDaemon stops ptpcamerad, the macOS daemon that claims
// cameras as soon as they appear and stops anything else opening them.
func releaseSystemCameraDaemon(ctx context.Context) error {
	return exec.CommandContext(ctx, "killall", "ptpcamerad").Run()
}

// clampInt32 narrows a value to the C int libusb expects, saturating rather
// than wrapping so a huge request becomes a large one instead of a negative.
func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}
