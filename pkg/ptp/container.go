// Package ptp implements just enough of PTP over USB (PIMA 15740) to drive a
// camera directly, without going through libgphoto2. Talking to the device
// ourselves is what makes it possible to issue Canon's vendor opcodes, which
// libgphoto2 does not expose.
package ptp

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Container types, PIMA 15740 clause 9.
const (
	ContainerCommand  uint16 = 1
	ContainerData     uint16 = 2
	ContainerResponse uint16 = 3
	ContainerEvent    uint16 = 4
)

// HeaderLen is the size of a container header: length, type, code, transaction.
const HeaderLen = 12

// maxPayloadLen bounds what will fit in a container's uint32 length field. The
// bound is int32 rather than uint32 so it is expressible on 32 bit builds; no
// real PTP payload comes close either way.
const maxPayloadLen = math.MaxInt32 - HeaderLen

// Standard operation codes.
const (
	OpGetDeviceInfo      uint16 = 0x1001
	OpOpenSession        uint16 = 0x1002
	OpCloseSession       uint16 = 0x1003
	OpGetDevicePropDesc  uint16 = 0x1014
	OpGetDevicePropValue uint16 = 0x1015
	OpSetDevicePropValue uint16 = 0x1016
)

// Standard response codes.
const (
	RCUndefined               uint16 = 0x2000
	RCOK                      uint16 = 0x2001
	RCGeneralError            uint16 = 0x2002
	RCSessionNotOpen          uint16 = 0x2003
	RCInvalidTransactionID    uint16 = 0x2004
	RCOperationNotSupported   uint16 = 0x2005
	RCParameterNotSupported   uint16 = 0x2006
	RCIncompleteTransfer      uint16 = 0x2007
	RCInvalidStorageID        uint16 = 0x2008
	RCInvalidObjectHandle     uint16 = 0x2009
	RCDevicePropNotSupported  uint16 = 0x200a
	RCInvalidObjectFormatCode uint16 = 0x200b
	RCStoreFull               uint16 = 0x200c
	RCAccessDenied            uint16 = 0x200f
	RCInvalidParentObject     uint16 = 0x2018
	RCDeviceBusy              uint16 = 0x2019
	RCInvalidParameter        uint16 = 0x201d
	RCSessionAlreadyOpen      uint16 = 0x201e
	RCTransactionCancelled    uint16 = 0x201f
)

var responseNames = map[uint16]string{
	RCUndefined:               "Undefined",
	RCOK:                      "OK",
	RCGeneralError:            "GeneralError",
	RCSessionNotOpen:          "SessionNotOpen",
	RCInvalidTransactionID:    "InvalidTransactionID",
	RCOperationNotSupported:   "OperationNotSupported",
	RCParameterNotSupported:   "ParameterNotSupported",
	RCIncompleteTransfer:      "IncompleteTransfer",
	RCInvalidStorageID:        "InvalidStorageID",
	RCInvalidObjectHandle:     "InvalidObjectHandle",
	RCDevicePropNotSupported:  "DevicePropNotSupported",
	RCInvalidObjectFormatCode: "InvalidObjectFormatCode",
	RCStoreFull:               "StoreFull",
	RCAccessDenied:            "AccessDenied",
	RCInvalidParentObject:     "InvalidParentObject",
	RCDeviceBusy:              "DeviceBusy",
	RCInvalidParameter:        "InvalidParameter",
	RCSessionAlreadyOpen:      "SessionAlreadyOpen",
	RCTransactionCancelled:    "TransactionCancelled",
}

// ResponseName returns a human readable name for a response code.
func ResponseName(code uint16) string {
	if name, ok := responseNames[code]; ok {
		return name
	}

	return fmt.Sprintf("%#04x", code)
}

// Container is a single PTP container: a header plus its payload.
type Container struct {
	Type          uint16
	Code          uint16
	TransactionID uint32
	Payload       []byte
}

// MarshalBinary renders the container as it goes on the wire.
func (c Container) MarshalBinary() ([]byte, error) {
	if len(c.Payload) > maxPayloadLen {
		return nil, fmt.Errorf("container payload of %d bytes does not fit a PTP length field", len(c.Payload))
	}

	length := HeaderLen + len(c.Payload)

	out := make([]byte, length)

	//nolint:gosec // maxPayloadLen above bounds this to a positive int32
	binary.LittleEndian.PutUint32(out[0:4], uint32(length))
	binary.LittleEndian.PutUint16(out[4:6], c.Type)
	binary.LittleEndian.PutUint16(out[6:8], c.Code)
	binary.LittleEndian.PutUint32(out[8:12], c.TransactionID)
	copy(out[HeaderLen:], c.Payload)

	return out, nil
}

// Params interprets the payload as the uint32 parameter array that command and
// response containers carry.
func (c Container) Params() []uint32 {
	params := make([]uint32, 0, len(c.Payload)/4)
	for off := 0; off+4 <= len(c.Payload); off += 4 {
		params = append(params, binary.LittleEndian.Uint32(c.Payload[off:off+4]))
	}

	return params
}

// ParseHeader reads a container header and returns the total container length.
func ParseHeader(b []byte) (Container, int, error) {
	if len(b) < HeaderLen {
		return Container{}, 0, fmt.Errorf("short container header: %d bytes", len(b))
	}

	length := int(binary.LittleEndian.Uint32(b[0:4]))
	if length < HeaderLen {
		return Container{}, 0, fmt.Errorf("container claims %d bytes, less than a header", length)
	}

	return Container{
		Type:          binary.LittleEndian.Uint16(b[4:6]),
		Code:          binary.LittleEndian.Uint16(b[6:8]),
		TransactionID: binary.LittleEndian.Uint32(b[8:12]),
	}, length, nil
}

// MarshalParams renders operation parameters as a container payload.
func MarshalParams(params ...uint32) []byte {
	out := make([]byte, 4*len(params))
	for i, p := range params {
		binary.LittleEndian.PutUint32(out[4*i:4*i+4], p)
	}

	return out
}
