package usb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// USB descriptor types, from the USB 2.0 specification, table 9-5.
const (
	descriptorTypeDevice    uint8 = 0x01
	descriptorTypeConfig    uint8 = 0x02
	descriptorTypeString    uint8 = 0x03
	descriptorTypeInterface uint8 = 0x04
	descriptorTypeEndpoint  uint8 = 0x05
)

// Endpoint transfer types, from bmAttributes bits 1:0.
const (
	transferTypeControl     uint8 = 0
	transferTypeIsochronous uint8 = 1
	transferTypeBulk        uint8 = 2
	transferTypeInterrupt   uint8 = 3
)

// endpointDirectionIn is bit 7 of bEndpointAddress.
const endpointDirectionIn uint8 = 0x80

// DeviceDescriptor is the subset of the USB device descriptor we care about.
type DeviceDescriptor struct {
	VendorID  uint16
	ProductID uint16
	Version   uint16

	Class    uint8
	SubClass uint8
	Protocol uint8

	ManufacturerIndex uint8
	ProductIndex      uint8
	SerialIndex       uint8

	NumConfigurations uint8
}

// Endpoint is one endpoint of an interface.
type Endpoint struct {
	Address       uint8
	Attributes    uint8
	MaxPacketSize uint16
}

// IsIn reports whether the endpoint carries data from the device to the host.
func (e Endpoint) IsIn() bool { return e.Address&endpointDirectionIn != 0 }

// IsBulk reports whether the endpoint is a bulk endpoint.
func (e Endpoint) IsBulk() bool { return e.Attributes&0x03 == transferTypeBulk }

// Interface is one alternate setting of one interface.
type Interface struct {
	Number    uint8
	Alternate uint8
	Class     uint8
	SubClass  uint8
	Protocol  uint8
	Endpoints []Endpoint
}

// BulkIn returns the interface's bulk IN endpoint.
func (i Interface) BulkIn() (Endpoint, bool) {
	for _, e := range i.Endpoints {
		if e.IsBulk() && e.IsIn() {
			return e, true
		}
	}

	return Endpoint{}, false
}

// BulkOut returns the interface's bulk OUT endpoint.
func (i Interface) BulkOut() (Endpoint, bool) {
	for _, e := range i.Endpoints {
		if e.IsBulk() && !e.IsIn() {
			return e, true
		}
	}

	return Endpoint{}, false
}

// Config is a parsed configuration descriptor.
type Config struct {
	Value      uint8
	Interfaces []Interface
}

// StillImageInterface returns the first interface with the still image class,
// which is the one carrying PTP. Bodies that also expose a mass storage or
// vendor specific interface are why we look the class up rather than assuming
// interface zero.
func (c Config) StillImageInterface() (Interface, bool) {
	for _, i := range c.Interfaces {
		if i.Class == ClassStillImage {
			return i, true
		}
	}

	return Interface{}, false
}

// PTPInterface returns the interface to use for PTP: the still image one when
// the device declares it, otherwise the first interface that has both bulk
// endpoints. Some bodies mislabel the class but are otherwise conformant.
func (c Config) PTPInterface() (Interface, bool) {
	if i, ok := c.StillImageInterface(); ok {
		return i, true
	}

	for _, i := range c.Interfaces {
		_, hasIn := i.BulkIn()
		_, hasOut := i.BulkOut()

		if hasIn && hasOut {
			return i, true
		}
	}

	return Interface{}, false
}

// ParseDeviceDescriptor parses an 18 byte USB device descriptor.
func ParseDeviceDescriptor(b []byte) (DeviceDescriptor, error) {
	const deviceDescriptorLen = 18

	if len(b) < deviceDescriptorLen {
		return DeviceDescriptor{}, fmt.Errorf("device descriptor too short: %d bytes", len(b))
	}

	if b[1] != descriptorTypeDevice {
		return DeviceDescriptor{}, fmt.Errorf("not a device descriptor: type %#02x", b[1])
	}

	return DeviceDescriptor{
		Class:             b[4],
		SubClass:          b[5],
		Protocol:          b[6],
		VendorID:          binary.LittleEndian.Uint16(b[8:10]),
		ProductID:         binary.LittleEndian.Uint16(b[10:12]),
		Version:           binary.LittleEndian.Uint16(b[12:14]),
		ManufacturerIndex: b[14],
		ProductIndex:      b[15],
		SerialIndex:       b[16],
		NumConfigurations: b[17],
	}, nil
}

// ParseConfig parses a configuration descriptor and everything that follows
// it, which is how both usbfs and a GET_DESCRIPTOR control transfer hand it
// over: one config descriptor followed by its interface and endpoint
// descriptors, back to back.
//
// Unknown descriptor types (class specific ones, interface associations) are
// skipped on their bLength rather than treated as errors.
func ParseConfig(b []byte) (Config, error) {
	const configDescriptorLen = 9

	if len(b) < configDescriptorLen {
		return Config{}, fmt.Errorf("config descriptor too short: %d bytes", len(b))
	}

	if b[1] != descriptorTypeConfig {
		return Config{}, fmt.Errorf("not a config descriptor: type %#02x", b[1])
	}

	// A descriptor may arrive truncated; honour wTotalLength but never read
	// past what was actually handed over.
	total := min(int(binary.LittleEndian.Uint16(b[2:4])), len(b))

	cfg := Config{Value: b[5]}

	for off := int(b[0]); off+2 <= total; {
		length := int(b[off])
		if length < 2 || off+length > total {
			break
		}

		switch b[off+1] {
		case descriptorTypeInterface:
			if length < 9 {
				return Config{}, fmt.Errorf("truncated interface descriptor at offset %d", off)
			}

			cfg.Interfaces = append(cfg.Interfaces, Interface{
				Number:    b[off+2],
				Alternate: b[off+3],
				Class:     b[off+5],
				SubClass:  b[off+6],
				Protocol:  b[off+7],
			})

		case descriptorTypeEndpoint:
			if length < 7 {
				return Config{}, fmt.Errorf("truncated endpoint descriptor at offset %d", off)
			}

			if len(cfg.Interfaces) == 0 {
				return Config{}, fmt.Errorf("endpoint descriptor at offset %d precedes any interface", off)
			}

			last := &cfg.Interfaces[len(cfg.Interfaces)-1]
			last.Endpoints = append(last.Endpoints, Endpoint{
				Address:       b[off+2],
				Attributes:    b[off+3],
				MaxPacketSize: binary.LittleEndian.Uint16(b[off+4 : off+6]),
			})
		}

		off += length
	}

	if len(cfg.Interfaces) == 0 {
		return Config{}, errors.New("configuration declares no interfaces")
	}

	return cfg, nil
}

// decodeStringDescriptor turns a USB string descriptor, which is UTF-16LE
// prefixed by its length and type, into a Go string.
func decodeStringDescriptor(b []byte) string {
	if len(b) < 2 || b[1] != descriptorTypeString {
		return ""
	}

	length := min(int(b[0]), len(b))

	units := make([]uint16, 0, (length-2)/2)
	for off := 2; off+2 <= length; off += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[off:off+2]))
	}

	return utf16Decode(units)
}

// utf16Decode converts UTF-16 code units to a string. Descriptors are short
// and ASCII in practice, but surrogate pairs are handled for correctness.
func utf16Decode(units []uint16) string {
	runes := make([]rune, 0, len(units))

	for i := 0; i < len(units); i++ {
		u := units[i]

		switch {
		case u >= 0xd800 && u < 0xdc00 && i+1 < len(units) &&
			units[i+1] >= 0xdc00 && units[i+1] < 0xe000:
			runes = append(runes, ((rune(u)-0xd800)<<10|(rune(units[i+1])-0xdc00))+0x10000)
			i++
		case u >= 0xd800 && u < 0xe000:
			runes = append(runes, '�')
		default:
			runes = append(runes, rune(u))
		}
	}

	return string(runes)
}
