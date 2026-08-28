package canon

import (
	"errors"
	"fmt"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
)

// PropShutterCounter is the PTP device property Canon exposed as a shutter
// counter up to roughly the DIGIC 5 generation, and removed afterwards.
// libgphoto2 reads it as /main/status/shuttercounter.
const PropShutterCounter uint16 = 0xd1ac

// ErrLegacyCounterUnsupported means the body does not carry property 0xD1AC,
// which is the expected answer on DIGIC 6 and newer.
var ErrLegacyCounterUnsupported = errors.New("the camera does not expose the legacy shutter counter property")

// minCounterReply is the shortest reply DecodeCounter can make sense of: a
// high byte at offset 1 and at least one byte that could hold the low byte.
const minCounterReply = 3

// LegacyShutterCounter reads property 0xD1AC.
//
// This is the free path that already works on a 5D Mark III and every older
// body, and it is worth trying first: it is a standard property read, it needs
// no vendor opcodes, and when it answers there is nothing more to do.
func LegacyShutterCounter(dev *ptp.Device) (uint32, error) {
	resp, err := dev.Transact(ptp.OpGetDevicePropValue, uint32(PropShutterCounter))
	if err != nil {
		return 0, err
	}

	switch resp.Code {
	case ptp.RCOK:
	case ptp.RCOperationNotSupported, ptp.RCDevicePropNotSupported, ptp.RCParameterNotSupported:
		return 0, ErrLegacyCounterUnsupported
	default:
		return 0, resp.Err()
	}

	if len(resp.Data) < 4 {
		return 0, fmt.Errorf("legacy counter returned %d bytes, expected 4", len(resp.Data))
	}

	return uint32(resp.Data[0]) | uint32(resp.Data[1])<<8 | uint32(resp.Data[2])<<16 | uint32(resp.Data[3])<<24, nil
}

// DecodeCounter turns an MPU reply into a counter value.
//
// The encoding is a high byte at offset 1 and a low byte in the tail:
//
//	value = reply[1]<<8 | low
//
// The low byte's offset varies between replies, observed at 7, 8 and 9, so it
// is taken as the last non-zero byte after offset 1. That is an empirical rule
// rather than an understood format, and it is what the carry test confirms: at
// MPU 0x1001 the low byte read 242, twenty frames later the high byte had
// stepped from 0x66 to 0x67 and the low byte read 6, which is exactly the
// arithmetic 242 + 20 = 262 demands. A wrong reading of the format would not
// carry correctly.
func DecodeCounter(reply []byte) (uint32, error) {
	if len(reply) < minCounterReply {
		return 0, fmt.Errorf("counter reply is %d bytes, expected at least %d", len(reply), minCounterReply)
	}

	high := uint32(reply[1])

	var low uint32

	for _, b := range reply[2:] {
		if b != 0 {
			low = uint32(b)
		}
	}

	return high<<8 | low, nil
}

// CounterReading is one counter, read.
type CounterReading struct {
	Counter Counter
	Value   uint32
	Raw     []byte
	Err     error
}

// ReadCounters reads every counter of a model in one MPU monitor session.
//
// A counter that fails is reported in its Reading rather than aborting the
// run: on an unrecognised body some addresses may simply not answer, and the
// ones that do are still worth having.
func (s *Session) ReadCounters(counters []Counter) []CounterReading {
	readings := make([]CounterReading, 0, len(counters))

	for _, c := range counters {
		reading := CounterReading{Counter: c}

		reading.Raw, reading.Err = s.ReadMPU(c.Address, c.Length)
		if reading.Err == nil {
			if len(reading.Raw) != int(c.Length) {
				reading.Err = fmt.Errorf("read %d bytes at MPU %#04x, asked for %d", len(reading.Raw), c.Address, c.Length)
			} else {
				reading.Value, reading.Err = DecodeCounter(reading.Raw)
			}
		}

		readings = append(readings, reading)
	}

	return readings
}

// ReadModel opens the MPU monitor, reads a model's counters and closes it
// again.
func ReadModel(dev *ptp.Device, model Model) ([]CounterReading, error) {
	session := NewSession(dev)

	if err := session.Open(); err != nil {
		return nil, err
	}

	readings := session.ReadCounters(model.Counters)

	if err := session.Close(); err != nil {
		return readings, err
	}

	return readings, nil
}
