//go:build linux

package usb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The Linux backend talks to usbfs directly. libusb is not needed, nothing has
// to be installed, and no driver is replaced: /dev/bus/usb is the same
// interface libusb itself drives.

const (
	sysfsUSBDevices = "/sys/bus/usb/devices"
	devBusUSB       = "/dev/bus/usb"

	// maxUSBFSTransfer is the kernel's per call ceiling for a synchronous
	// usbfs bulk transfer (MAX_USBFS_BUFFER_SIZE in drivers/usb/core/devio.c).
	// Larger transfers are split across several ioctls.
	maxUSBFSTransfer = 16 * 1024
)

// ioctl direction bits, from include/uapi/asm-generic/ioctl.h. Every
// architecture Go builds Linux binaries for that we care about (386, amd64,
// arm, arm64, riscv64) uses this encoding; the handful that do not (mips,
// powerpc, sparc, alpha) would need their own shifts.
const (
	iocNRBits   = 8
	iocTypeBits = 8
	iocSizeBits = 14

	iocNRShift   = 0
	iocTypeShift = iocNRShift + iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits

	iocWrite = 1
	iocRead  = 2
)

// usbfsIoc builds a usbfs ioctl request number. Every usbfs ioctl is typed
// 'U', so only the direction, the number and the argument size vary.
func usbfsIoc(dir, nr uint, size uintptr) uintptr {
	const usbfsIocType = 'U'

	return uintptr(dir)<<iocDirShift |
		uintptr(usbfsIocType)<<iocTypeShift |
		uintptr(nr)<<iocNRShift |
		size<<iocSizeShift
}

// usbfs ioctls. The size encoded in the request number is the size of the
// argument struct on this architecture, so these are computed rather than
// hard coded: usbdevfs_bulktransfer is 24 bytes on 64 bit and 16 on 32 bit.
var (
	iocClaimInterface   = usbfsIoc(iocRead, 15, unsafe.Sizeof(uint32(0)))
	iocReleaseInterface = usbfsIoc(iocRead, 16, unsafe.Sizeof(uint32(0)))
	iocClearHalt        = usbfsIoc(iocRead, 21, unsafe.Sizeof(uint32(0)))
	iocBulk             = usbfsIoc(iocRead|iocWrite, 2, unsafe.Sizeof(bulkTransfer{}))
	iocDisconnectClaim  = usbfsIoc(iocRead|iocWrite, 27, unsafe.Sizeof(disconnectClaim{}))
)

// bulkTransfer mirrors struct usbdevfs_bulktransfer. Go's natural alignment of
// uintptr reproduces the C layout on both 32 and 64 bit: 16 bytes there, 24
// here, with the pointer aligned to its own width.
type bulkTransfer struct {
	endpoint uint32
	length   uint32
	timeout  uint32
	data     uintptr
}

// disconnectClaim mirrors struct usbdevfs_disconnect_claim.
type disconnectClaim struct {
	iface  uint32
	flags  uint32
	driver [256]byte
}

// linuxDevice is a claimed usbfs interface.
type linuxDevice struct {
	file  *os.File
	info  DeviceInfo
	iface uint8
	epIn  uint8
	epOut uint8
}

func list() ([]DeviceInfo, error) {
	entries, err := os.ReadDir(sysfsUSBDevices)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", sysfsUSBDevices, err)
	}

	var devices []DeviceInfo

	for _, e := range entries {
		// Interface directories are named "1-2:1.0"; skip them, along with
		// the root hubs named "usb1".
		name := e.Name()
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue
		}

		info, ok := readSysfsDevice(filepath.Join(sysfsUSBDevices, name))
		if !ok {
			continue
		}

		devices = append(devices, info)
	}

	return devices, nil
}

// readSysfsDevice builds a DeviceInfo from a sysfs USB device directory,
// reporting false when the device does not expose a still image interface.
func readSysfsDevice(dir string) (DeviceInfo, bool) {
	vendor, err := readHexAttr(dir, "idVendor")
	if err != nil {
		return DeviceInfo{}, false
	}

	product, err := readHexAttr(dir, "idProduct")
	if err != nil {
		return DeviceInfo{}, false
	}

	bus, err := readDecAttr(dir, "busnum")
	if err != nil {
		return DeviceInfo{}, false
	}

	address, err := readDecAttr(dir, "devnum")
	if err != nil {
		return DeviceInfo{}, false
	}

	if !hasStillImageInterface(dir) {
		return DeviceInfo{}, false
	}

	return DeviceInfo{
		Path:         fmt.Sprintf("%s/%03d/%03d", devBusUSB, bus, address),
		Bus:          bus,
		Address:      address,
		VendorID:     vendor,
		ProductID:    product,
		Manufacturer: readStringAttr(dir, "manufacturer"),
		Product:      readStringAttr(dir, "product"),
		Serial:       readStringAttr(dir, "serial"),
	}, true
}

// hasStillImageInterface reports whether any of the device's interfaces
// declares the still image class.
func hasStillImageInterface(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	base := filepath.Base(dir)

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), base+":") {
			continue
		}

		class, err := readHexAttr(filepath.Join(dir, e.Name()), "bInterfaceClass")
		if err == nil && class == uint16(ClassStillImage) {
			return true
		}
	}

	return false
}

func readAttr(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(b)), nil
}

func readStringAttr(dir, name string) string {
	v, err := readAttr(dir, name)
	if err != nil {
		return ""
	}

	return v
}

// readHexAttr reads a sysfs attribute written as hexadecimal, such as
// idVendor or bInterfaceClass.
func readHexAttr(dir, name string) (uint16, error) {
	v, err := readAttr(dir, name)
	if err != nil {
		return 0, err
	}

	parsed, err := strconv.ParseUint(v, 16, 16)

	return uint16(parsed), err
}

// readDecAttr reads a sysfs attribute written as decimal, such as busnum.
func readDecAttr(dir, name string) (uint16, error) {
	v, err := readAttr(dir, name)
	if err != nil {
		return 0, err
	}

	parsed, err := strconv.ParseUint(v, 10, 16)

	return uint16(parsed), err
}

func open(info DeviceInfo) (Device, error) {
	descriptors, err := readDescriptors(info)
	if err != nil {
		return nil, err
	}

	if len(descriptors) < 18 {
		return nil, fmt.Errorf("device descriptor is %d bytes, expected at least 18", len(descriptors))
	}

	cfg, err := ParseConfig(descriptors[18:])
	if err != nil {
		return nil, fmt.Errorf("parsing configuration descriptor: %w", err)
	}

	iface, ok := cfg.PTPInterface()
	if !ok {
		return nil, fmt.Errorf("%s exposes no still image interface", info.Path)
	}

	epIn, okIn := iface.BulkIn()
	epOut, okOut := iface.BulkOut()

	if !okIn || !okOut {
		return nil, fmt.Errorf("interface %d has no bulk endpoint pair", iface.Number)
	}

	file, err := os.OpenFile(info.Path, os.O_RDWR, 0)
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("opening %s: %w (run as root, or install the udev rule from the README)", info.Path, err)
		}

		return nil, fmt.Errorf("opening %s: %w", info.Path, err)
	}

	d := &linuxDevice{
		file:  file,
		info:  info,
		iface: iface.Number,
		epIn:  epIn.Address,
		epOut: epOut.Address,
	}

	if err := d.claim(); err != nil {
		_ = file.Close()

		return nil, err
	}

	// A run that crashed mid transfer leaves the endpoints halted and every
	// subsequent transfer fails until they are cleared.
	_ = d.ClearHalt()

	return d, nil
}

// readDescriptors returns the device descriptor followed by the configuration
// descriptors, in bus order. sysfs exposes them ready made; falling back to the
// usbfs node covers a system without sysfs mounted.
func readDescriptors(info DeviceInfo) ([]byte, error) {
	pattern := filepath.Join(sysfsUSBDevices, "*", "descriptors")

	matches, err := filepath.Glob(pattern)
	if err == nil {
		for _, m := range matches {
			dir := filepath.Dir(m)

			bus, errBus := readDecAttr(dir, "busnum")
			addr, errAddr := readDecAttr(dir, "devnum")

			if errBus != nil || errAddr != nil {
				continue
			}

			if bus != info.Bus || addr != info.Address {
				continue
			}

			if b, err := os.ReadFile(m); err == nil {
				return b, nil
			}
		}
	}

	b, err := os.ReadFile(info.Path)
	if err != nil {
		return nil, fmt.Errorf("reading descriptors from %s: %w", info.Path, err)
	}

	return b, nil
}

func (d *linuxDevice) Info() DeviceInfo { return d.info }

func (d *linuxDevice) Read(p []byte, timeout time.Duration) (int, error) {
	return d.bulk(d.epIn, p, timeout)
}

func (d *linuxDevice) Write(p []byte, timeout time.Duration) (int, error) {
	return d.bulk(d.epOut, p, timeout)
}

func (d *linuxDevice) ClearHalt() error {
	var errs []error

	for _, ep := range []uint8{d.epIn, d.epOut} {
		endpoint := uint32(ep)
		if err := d.ioctl(iocClearHalt, unsafe.Pointer(&endpoint)); err != nil {
			errs = append(errs, fmt.Errorf("clearing halt on endpoint %#02x: %w", ep, err))
		}
	}

	return errors.Join(errs...)
}

func (d *linuxDevice) Close() error {
	iface := uint32(d.iface)
	_ = d.ioctl(iocReleaseInterface, unsafe.Pointer(&iface))

	return d.file.Close()
}

// claim takes the interface, disconnecting any kernel driver bound to it.
//
// A userspace holder such as gvfs cannot be displaced this way, which is the
// common case on a desktop and the reason for the hint in the error.
func (d *linuxDevice) claim() error {
	dc := disconnectClaim{iface: uint32(d.iface)}

	err := d.ioctl(iocDisconnectClaim, unsafe.Pointer(&dc))
	if err == nil {
		return nil
	}

	// Older kernels have no DISCONNECT_CLAIM; fall back to a plain claim.
	if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) {
		iface := uint32(d.iface)

		if err = d.ioctl(iocClaimInterface, unsafe.Pointer(&iface)); err == nil {
			return nil
		}
	}

	if errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("claiming interface %d on %s: %w: %w", d.iface, d.info.Path, ErrBusy, err)
	}

	return fmt.Errorf("claiming interface %d on %s: %w", d.iface, d.info.Path, err)
}

func (d *linuxDevice) ioctl(request uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.file.Fd(), request, uintptr(arg))
		if errno == 0 {
			return nil
		}

		if errno == unix.EINTR {
			continue
		}

		return errno
	}
}

// bulk runs one usbfs bulk transfer and returns the number of bytes moved.
func (d *linuxDevice) bulk(endpoint uint8, p []byte, timeout time.Duration) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	length := min(len(p), maxUSBFSTransfer)

	bt := bulkTransfer{
		endpoint: uint32(endpoint),
		length:   uint32(length),
		timeout:  clampUint32(timeout.Milliseconds()),
		data:     uintptr(unsafe.Pointer(&p[0])),
	}

	for {
		n, _, errno := unix.Syscall(unix.SYS_IOCTL, d.file.Fd(), iocBulk, uintptr(unsafe.Pointer(&bt)))

		runtime.KeepAlive(p)

		if errno == 0 {
			return int(n), nil
		}

		if errno == unix.EINTR {
			continue
		}

		return 0, errno
	}
}

// releaseSystemCameraDaemon has nothing to do on Linux. Cameras are held by
// userspace processes such as gvfs rather than by a system daemon we can stop,
// and the interface claim reports that case to the user instead.
func releaseSystemCameraDaemon(_ context.Context) error { return nil }
