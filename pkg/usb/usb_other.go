//go:build !linux && !darwin && !windows

package usb

import "context"

// No backend exists for this platform yet. Adding one means implementing list
// and open against whatever the operating system offers for raw USB bulk
// transfers; everything above pkg/usb is portable.

func list() ([]DeviceInfo, error) {
	return nil, ErrNotSupported
}

func open(_ DeviceInfo) (Device, error) {
	return nil, ErrNotSupported
}

func releaseSystemCameraDaemon(_ context.Context) error { return nil }
