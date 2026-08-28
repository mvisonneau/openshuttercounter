//go:build windows

package usb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows backend opens the still image device interface that usbscan.sys
// exposes for every PTP camera, and exchanges PTP containers with ReadFile and
// WriteFile. Canon's stock driver is enough: nothing has to be replaced with
// WinUSB, so EOS Utility keeps working alongside this.
//
// This mirrors what static analysis of a working vendor tool suggests it does
// (SetupAPI or WMI to find the interface path, then CreateFileA, ReadFile and
// WriteFile, with no DeviceIoControl anywhere in its import table). It has not
// yet been confirmed against hardware; see docs/PROTOCOL.md.

// guidDevInterfaceImage is GUID_DEVINTERFACE_IMAGE,
// {6BDD1FC6-810F-11D0-BEC7-08002BE2092F}, the still image device interface
// class every PTP camera registers under.
var guidDevInterfaceImage = windows.GUID{
	Data1: 0x6bdd1fc6,
	Data2: 0x810f,
	Data3: 0x11d0,
	Data4: [8]byte{0xbe, 0xc7, 0x08, 0x00, 0x2b, 0xe2, 0x09, 0x2f},
}

var (
	modSetupAPI = windows.NewLazySystemDLL("setupapi.dll")

	procSetupDiGetClassDevsW             = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	digcfPresent         = 0x00000002
	digcfDeviceInterface = 0x00000010

	// minInterfaceDetailSize is the smallest plausible
	// SP_DEVICE_INTERFACE_DETAIL_DATA_W: the cbSize field plus a terminator.
	minInterfaceDetailSize = 8
)

// spDeviceInterfaceData mirrors SP_DEVICE_INTERFACE_DATA.
type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGUID windows.GUID
	flags              uint32
	reserved           uintptr
}

func list() ([]DeviceInfo, error) {
	set, _, err := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guidDevInterfaceImage)),
		0, 0,
		uintptr(digcfPresent|digcfDeviceInterface),
	)
	if set == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
	}

	defer func() { _, _, _ = procSetupDiDestroyDeviceInfoList.Call(set) }()

	var devices []DeviceInfo

	for index := uint32(0); ; index++ {
		data := spDeviceInterfaceData{cbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}

		ok, _, err := procSetupDiEnumDeviceInterfaces.Call(
			set, 0,
			uintptr(unsafe.Pointer(&guidDevInterfaceImage)),
			uintptr(index),
			uintptr(unsafe.Pointer(&data)),
		)
		if ok == 0 {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}

			return nil, fmt.Errorf("SetupDiEnumDeviceInterfaces: %w", err)
		}

		path, err := interfaceDetail(set, &data)
		if err != nil {
			return nil, err
		}

		devices = append(devices, deviceInfoFromPath(path))
	}

	return devices, nil
}

// interfaceDetail resolves a device interface to the path CreateFile wants.
func interfaceDetail(set uintptr, data *spDeviceInterfaceData) (string, error) {
	var required uint32

	// The first call is expected to fail with ERROR_INSUFFICIENT_BUFFER. All
	// it is asked for is the size the detail structure needs, so the failure
	// only matters if it came back without one.
	if _, _, err := procSetupDiGetDeviceInterfaceDetailW.Call(
		set,
		uintptr(unsafe.Pointer(data)),
		0, 0,
		uintptr(unsafe.Pointer(&required)),
		0,
	); required < minInterfaceDetailSize {
		return "", fmt.Errorf(
			"SetupDiGetDeviceInterfaceDetail reported a size of %d bytes: %w", required, err,
		)
	}

	buf := make([]byte, required)

	// SP_DEVICE_INTERFACE_DETAIL_DATA_W is { DWORD cbSize; WCHAR
	// DevicePath[ANYSIZE_ARRAY]; }. cbSize must describe the fixed part of
	// the structure, which the alignment of WCHAR makes 8 bytes on 64 bit
	// and 6 on 32 bit, not the size of the whole buffer.
	cbSize := uint32(8)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		cbSize = 6
	}

	*(*uint32)(unsafe.Pointer(&buf[0])) = cbSize

	ok, _, err := procSetupDiGetDeviceInterfaceDetailW.Call(
		set,
		uintptr(unsafe.Pointer(data)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(required),
		uintptr(unsafe.Pointer(&required)),
		0,
	)
	if ok == 0 {
		return "", fmt.Errorf("SetupDiGetDeviceInterfaceDetail: %w", err)
	}

	// The path is the UTF-16 string that follows cbSize.
	chars := (required - 4) / 2
	utf16Path := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), chars)

	return windows.UTF16ToString(utf16Path), nil
}

// deviceInfoFromPath pulls the identity out of an interface path, which looks
// like \\?\usb#vid_04a9&pid_3281#0123456789#{6bdd1fc6-...}.
func deviceInfoFromPath(path string) DeviceInfo {
	info := DeviceInfo{Path: path}

	lower := strings.ToLower(path)

	if id, ok := hexField(lower, "vid_"); ok {
		info.VendorID = id
	}

	if id, ok := hexField(lower, "pid_"); ok {
		info.ProductID = id
	}

	if fields := strings.Split(path, "#"); len(fields) >= 3 {
		info.Serial = fields[2]
	}

	return info
}

func hexField(s, prefix string) (uint16, bool) {
	i := strings.Index(s, prefix)
	if i < 0 || len(s) < i+len(prefix)+4 {
		return 0, false
	}

	v, err := strconv.ParseUint(s[i+len(prefix):i+len(prefix)+4], 16, 16)
	if err != nil {
		return 0, false
	}

	return uint16(v), true
}

// windowsDevice is an opened still image device interface.
type windowsDevice struct {
	handle windows.Handle
	info   DeviceInfo
}

func open(info DeviceInfo) (Device, error) {
	path, err := windows.UTF16PtrFromString(info.Path)
	if err != nil {
		return nil, fmt.Errorf("device path %q: %w", info.Path, err)
	}

	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("opening %s: %w: %w (close EOS Utility and any other camera software)", info.Path, ErrBusy, err)
		}

		return nil, fmt.Errorf("opening %s: %w", info.Path, err)
	}

	return &windowsDevice{handle: handle, info: info}, nil
}

func (d *windowsDevice) Info() DeviceInfo { return d.info }

func (d *windowsDevice) Read(p []byte, timeout time.Duration) (int, error) {
	return d.transfer(p, timeout, false)
}

func (d *windowsDevice) Write(p []byte, timeout time.Duration) (int, error) {
	return d.transfer(p, timeout, true)
}

// ClearHalt is a no-op on Windows: usbscan.sys owns the pipes and resets them
// itself, and the interface exposes no way to ask for it.
func (d *windowsDevice) ClearHalt() error { return nil }

func (d *windowsDevice) Close() error {
	return windows.CloseHandle(d.handle)
}

// transfer runs one overlapped read or write, cancelling it if the camera does
// not answer within the timeout. Without the cancellation a stalled endpoint
// would hang the process indefinitely.
func (d *windowsDevice) transfer(p []byte, timeout time.Duration, write bool) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, fmt.Errorf("creating overlapped event: %w", err)
	}

	defer func() { _ = windows.CloseHandle(event) }()

	overlapped := windows.Overlapped{HEvent: event}

	var done uint32

	if write {
		err = windows.WriteFile(d.handle, p, &done, &overlapped)
	} else {
		err = windows.ReadFile(d.handle, p, &done, &overlapped)
	}

	if errors.Is(err, windows.ERROR_IO_PENDING) {
		milliseconds := uint32(windows.INFINITE)
		if timeout > 0 {
			milliseconds = clampUint32(timeout.Milliseconds())
		}

		state, waitErr := windows.WaitForSingleObject(event, milliseconds)
		if waitErr != nil {
			return 0, fmt.Errorf("waiting on transfer: %w", waitErr)
		}

		if state != windows.WAIT_OBJECT_0 {
			_ = windows.CancelIoEx(d.handle, &overlapped)

			return 0, fmt.Errorf("transfer timed out after %s", timeout)
		}

		err = windows.GetOverlappedResult(d.handle, &overlapped, &done, false)
	}

	if err != nil {
		return int(done), fmt.Errorf("bulk transfer: %w", err)
	}

	return int(done), nil
}

// releaseSystemCameraDaemon has nothing to do on Windows: the still image
// device interface is shared, and whatever else has it open is an application
// the user can close.
func releaseSystemCameraDaemon(_ context.Context) error { return nil }
