package ptp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unicode/utf16"
)

// Standard operation codes for reading what a camera holds. Every one of them
// is read only: nothing here can change, move or delete an object.
const (
	OpGetStorageIDs     uint16 = 0x1004
	OpGetObjectHandles  uint16 = 0x1007
	OpGetObjectInfo     uint16 = 0x1008
	OpGetObject         uint16 = 0x1009
	OpGetPartialObject  uint16 = 0x101b
	AllStorages         uint32 = 0xffffffff
	AllFormats          uint32 = 0x00000000
	RootAndBelow        uint32 = 0x00000000
	ObjectFormatEXIFJPG uint16 = 0x3801
)

// ErrShortData is returned when a dataset ends before its fields do.
var ErrShortData = errors.New("ptp dataset is truncated")

// DeviceInfo is the GetDeviceInfo dataset, PIMA 15740 clause 5.5.1.
type DeviceInfo struct {
	StandardVersion      uint16   `json:"standard_version"`
	VendorExtensionID    uint32   `json:"vendor_extension_id"`
	VendorExtensionVer   uint16   `json:"vendor_extension_version"`
	VendorExtensionDesc  string   `json:"vendor_extension_desc,omitempty"`
	FunctionalMode       uint16   `json:"functional_mode"`
	OperationsSupported  []uint16 `json:"operations_supported"`
	EventsSupported      []uint16 `json:"events_supported"`
	DevicePropsSupported []uint16 `json:"device_props_supported"`
	CaptureFormats       []uint16 `json:"capture_formats"`
	ImageFormats         []uint16 `json:"image_formats"`
	Manufacturer         string   `json:"manufacturer,omitempty"`
	Model                string   `json:"model,omitempty"`
	DeviceVersion        string   `json:"device_version,omitempty"`
	SerialNumber         string   `json:"serial_number,omitempty"`
}

// Supports reports whether the device advertises an operation.
func (i DeviceInfo) Supports(op uint16) bool {
	return slices.Contains(i.OperationsSupported, op)
}

// ObjectInfo is the GetObjectInfo dataset, PIMA 15740 clause 5.5.2, cut down
// to the fields a reader has any use for.
type ObjectInfo struct {
	StorageID        uint32 `json:"storage_id"`
	Format           uint16 `json:"format"`
	CompressedSize   uint32 `json:"compressed_size"`
	ParentObject     uint32 `json:"parent_object"`
	AssociationType  uint16 `json:"association_type"`
	Filename         string `json:"filename"`
	CaptureDate      string `json:"capture_date,omitempty"`
	ModificationDate string `json:"modification_date,omitempty"`
}

// GetDeviceInfo asks the device to describe itself.
func (d *Device) GetDeviceInfo() (DeviceInfo, error) {
	resp, err := d.Transact(OpGetDeviceInfo)
	if err != nil {
		return DeviceInfo{}, err
	}

	if err := resp.Err(); err != nil {
		return DeviceInfo{}, fmt.Errorf("GetDeviceInfo: %w", err)
	}

	return ParseDeviceInfo(resp.Data)
}

// GetStorageIDs lists the stores the device holds, typically one per card.
func (d *Device) GetStorageIDs() ([]uint32, error) {
	resp, err := d.Transact(OpGetStorageIDs)
	if err != nil {
		return nil, err
	}

	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("GetStorageIDs: %w", err)
	}

	r := reader{b: resp.Data}

	return r.u32Array(), r.err
}

// GetObjectHandles lists the objects in a store. format 0 means any format;
// parent 0 means every object in the store, whatever folder it is in.
func (d *Device) GetObjectHandles(storage, format, parent uint32) ([]uint32, error) {
	resp, err := d.Transact(OpGetObjectHandles, storage, format, parent)
	if err != nil {
		return nil, err
	}

	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("GetObjectHandles: %w", err)
	}

	r := reader{b: resp.Data}

	return r.u32Array(), r.err
}

// GetObjectInfo describes one object.
func (d *Device) GetObjectInfo(handle uint32) (ObjectInfo, error) {
	resp, err := d.Transact(OpGetObjectInfo, handle)
	if err != nil {
		return ObjectInfo{}, err
	}

	if err := resp.Err(); err != nil {
		return ObjectInfo{}, fmt.Errorf("GetObjectInfo(%#x): %w", handle, err)
	}

	return ParseObjectInfo(resp.Data)
}

// GetPartialObject reads length bytes of an object starting at offset.
func (d *Device) GetPartialObject(handle, offset, length uint32) ([]byte, error) {
	resp, err := d.Transact(OpGetPartialObject, handle, offset, length)
	if err != nil {
		return nil, err
	}

	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("GetPartialObject(%#x): %w", handle, err)
	}

	return resp.Data, nil
}

// GetObject reads a whole object.
func (d *Device) GetObject(handle uint32) ([]byte, error) {
	resp, err := d.Transact(OpGetObject, handle)
	if err != nil {
		return nil, err
	}

	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("GetObject(%#x): %w", handle, err)
	}

	return resp.Data, nil
}

// ParseDeviceInfo decodes a GetDeviceInfo dataset.
func ParseDeviceInfo(b []byte) (DeviceInfo, error) {
	r := reader{b: b}

	info := DeviceInfo{
		StandardVersion:     r.u16(),
		VendorExtensionID:   r.u32(),
		VendorExtensionVer:  r.u16(),
		VendorExtensionDesc: r.str(),
		FunctionalMode:      r.u16(),
	}
	info.OperationsSupported = r.u16Array()
	info.EventsSupported = r.u16Array()
	info.DevicePropsSupported = r.u16Array()
	info.CaptureFormats = r.u16Array()
	info.ImageFormats = r.u16Array()
	info.Manufacturer = r.str()
	info.Model = r.str()
	info.DeviceVersion = r.str()
	info.SerialNumber = r.str()

	return info, r.err
}

// ParseObjectInfo decodes a GetObjectInfo dataset.
func ParseObjectInfo(b []byte) (ObjectInfo, error) {
	r := reader{b: b}

	var info ObjectInfo

	info.StorageID = r.u32()
	info.Format = r.u16()
	r.skip(2) // protection status
	info.CompressedSize = r.u32()
	r.skip(2 + 6*4) // thumb format, thumb size, thumb w/h, image w/h, bit depth
	info.ParentObject = r.u32()
	info.AssociationType = r.u16()
	r.skip(4 + 4) // association description, sequence number
	info.Filename = r.str()
	info.CaptureDate = r.str()
	info.ModificationDate = r.str()

	return info, r.err
}

// reader walks a little endian PTP dataset. The first failure sticks, so a
// caller can decode a whole dataset and check once at the end.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}

	if n < 0 || r.off+n > len(r.b) {
		r.err = fmt.Errorf("%w: wanted %d bytes at offset %d of %d", ErrShortData, n, r.off, len(r.b))

		return nil
	}

	out := r.b[r.off : r.off+n]
	r.off += n

	return out
}

func (r *reader) skip(n int) { r.take(n) }

func (r *reader) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}

	return 0
}

func (r *reader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}

	return 0
}

func (r *reader) u16Array() []uint16 {
	n := int(r.u32())

	b := r.take(2 * n)
	if b == nil {
		return nil
	}

	out := make([]uint16, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(b[2*i:])
	}

	return out
}

func (r *reader) u32Array() []uint32 {
	n := int(r.u32())

	b := r.take(4 * n)
	if b == nil {
		return nil
	}

	out := make([]uint32, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[4*i:])
	}

	return out
}

// str reads a PTP string: a one byte count of UTF-16 units, terminator
// included, followed by the units.
func (r *reader) str() string {
	b := r.take(1)
	if b == nil || b[0] == 0 {
		return ""
	}

	raw := r.take(2 * int(b[0]))
	if raw == nil {
		return ""
	}

	units := make([]uint16, 0, b[0])

	for i := 0; i+1 < len(raw); i += 2 {
		u := binary.LittleEndian.Uint16(raw[i:])
		if u == 0 {
			break
		}

		units = append(units, u)
	}

	return string(utf16.Decode(units))
}
