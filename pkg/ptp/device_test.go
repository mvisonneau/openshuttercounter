package ptp

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// fakeTransport replays a scripted sequence of bulk reads and records
// everything written, so a whole transaction can be checked without hardware.
type fakeTransport struct {
	reads  [][]byte
	writes [][]byte
	halts  int
}

func (f *fakeTransport) Info() usb.DeviceInfo { return usb.DeviceInfo{VendorID: 0x04a9} }

func (f *fakeTransport) Read(p []byte, _ time.Duration) (int, error) {
	if len(f.reads) == 0 {
		return 0, errors.New("fake transport ran out of scripted reads")
	}

	next := f.reads[0]
	f.reads = f.reads[1:]

	return copy(p, next), nil
}

func (f *fakeTransport) Write(p []byte, _ time.Duration) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), p...))

	return len(p), nil
}

func (f *fakeTransport) ClearHalt() error {
	f.halts++

	return nil
}

func (f *fakeTransport) Close() error { return nil }

func container(t *testing.T, c Container) []byte {
	t.Helper()

	raw, err := c.MarshalBinary()
	require.NoError(t, err)

	return raw
}

func TestContainerRoundTrip(t *testing.T) {
	original := Container{
		Type:          ContainerCommand,
		Code:          0x9052,
		TransactionID: 42,
		Payload:       MarshalParams(0, 1),
	}

	raw := container(t, original)
	require.Len(t, raw, HeaderLen+8)

	parsed, length, err := ParseHeader(raw)
	require.NoError(t, err)
	assert.Equal(t, len(raw), length)
	assert.Equal(t, original.Type, parsed.Type)
	assert.Equal(t, original.Code, parsed.Code)
	assert.Equal(t, original.TransactionID, parsed.TransactionID)

	parsed.Payload = raw[HeaderLen:]
	assert.Equal(t, []uint32{0, 1}, parsed.Params())
}

func TestTransactCollectsDataThenResponse(t *testing.T) {
	transport := &fakeTransport{
		reads: [][]byte{
			container(t, Container{Type: ContainerData, Code: 0x9053, TransactionID: 1, Payload: []byte{0x00, 0x66, 0xf2}}),
			container(t, Container{Type: ContainerResponse, Code: RCOK, TransactionID: 1}),
		},
	}

	resp, err := New(transport).Transact(0x9053, 0, 0, 1)
	require.NoError(t, err)
	assert.True(t, resp.OK())
	assert.Equal(t, []byte{0x00, 0x66, 0xf2}, resp.Data)

	require.Len(t, transport.writes, 1)

	sent, _, err := ParseHeader(transport.writes[0])
	require.NoError(t, err)
	assert.Equal(t, ContainerCommand, sent.Type)
	assert.Equal(t, uint16(0x9053), sent.Code)

	sent.Payload = transport.writes[0][HeaderLen:]
	assert.Equal(t, []uint32{0, 0, 1}, sent.Params())
}

// TestTransactOutSendsCommandThenData covers the shape the factory channel
// needs: a command container carrying the parameters, then a data container
// repeating the same opcode and transaction ID.
func TestTransactOutSendsCommandThenData(t *testing.T) {
	payload := []byte("MonOpen\x00")

	transport := &fakeTransport{
		reads: [][]byte{
			container(t, Container{Type: ContainerResponse, Code: RCOK, TransactionID: 1}),
		},
	}

	resp, err := New(transport).TransactOut(0x9052, payload, 0, 0)
	require.NoError(t, err)
	require.True(t, resp.OK())
	require.Len(t, transport.writes, 2)

	command, _, err := ParseHeader(transport.writes[0])
	require.NoError(t, err)
	assert.Equal(t, ContainerCommand, command.Type)

	data, length, err := ParseHeader(transport.writes[1])
	require.NoError(t, err)
	assert.Equal(t, ContainerData, data.Type)
	assert.Equal(t, command.Code, data.Code)
	assert.Equal(t, command.TransactionID, data.TransactionID)
	assert.Equal(t, payload, transport.writes[1][HeaderLen:length])
}

// TestReadContainerReassemblesSplitTransfers matters because a bulk read
// returns whatever the endpoint happened to deliver, not a whole container.
func TestReadContainerReassemblesSplitTransfers(t *testing.T) {
	whole := container(t, Container{
		Type:          ContainerData,
		Code:          0x9053,
		TransactionID: 7,
		Payload:       []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	})

	transport := &fakeTransport{reads: [][]byte{whole[:5], whole[5:14], whole[14:]}}

	got, err := New(transport).ReadContainer()
	require.NoError(t, err)
	assert.Equal(t, uint16(0x9053), got.Code)
	assert.Equal(t, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, got.Payload)
}

func TestTransactSkipsAsynchronousEvents(t *testing.T) {
	transport := &fakeTransport{
		reads: [][]byte{
			container(t, Container{Type: ContainerEvent, Code: 0xc189, TransactionID: 0}),
			container(t, Container{Type: ContainerResponse, Code: RCOK, TransactionID: 1}),
		},
	}

	resp, err := New(transport).Transact(OpGetDeviceInfo)
	require.NoError(t, err)
	assert.True(t, resp.OK())
}

func TestResponseErrors(t *testing.T) {
	resp := Response{Code: RCDeviceBusy}
	require.Error(t, resp.Err())
	assert.Contains(t, resp.Err().Error(), "DeviceBusy")
	require.ErrorIs(t, resp.Err(), &ResponseError{Code: RCDeviceBusy})
	require.NotErrorIs(t, resp.Err(), &ResponseError{Code: RCOK})

	assert.Equal(t, "OperationNotSupported", ResponseName(RCOperationNotSupported))
	assert.Equal(t, "0x1234", ResponseName(0x1234))
}

// TestOpenSessionToleratesAnAlreadyOpenSession covers the common case of a
// previous run that did not close cleanly.
func TestOpenSessionToleratesAnAlreadyOpenSession(t *testing.T) {
	transport := &fakeTransport{
		reads: [][]byte{
			container(t, Container{Type: ContainerResponse, Code: RCSessionAlreadyOpen, TransactionID: 1}),
			container(t, Container{Type: ContainerResponse, Code: RCOK, TransactionID: 2}),
		},
	}

	dev := New(transport)
	require.NoError(t, dev.OpenSession())
	require.NoError(t, dev.CloseSession())
	assert.Len(t, transport.writes, 2)
}

func TestCloseSessionWithoutOpenIsANoOp(t *testing.T) {
	transport := &fakeTransport{}
	require.NoError(t, New(transport).CloseSession())
	assert.Empty(t, transport.writes)
}

// TestReadContainerToleratesZeroLengthPackets covers the USB behaviour where a
// transfer whose payload is an exact multiple of the endpoint's packet size is
// terminated by an empty packet. Treating one of those as the end of the
// conversation would break reads on some bodies and not others.
func TestReadContainerToleratesZeroLengthPackets(t *testing.T) {
	whole := container(t, Container{
		Type:          ContainerData,
		Code:          0x9053,
		TransactionID: 3,
		Payload:       []byte{0x00, 0x66, 0x00, 0x00, 0xf2, 0x00},
	})

	transport := &fakeTransport{reads: [][]byte{whole[:6], {}, whole[6:]}}

	got, err := New(transport).ReadContainer()
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00, 0x66, 0x00, 0x00, 0xf2, 0x00}, got.Payload)
}

func TestReadContainerGivesUpOnAnEndlesslyEmptyDevice(t *testing.T) {
	transport := &fakeTransport{reads: [][]byte{{}, {}, {}, {}, {}, {}}}

	_, err := New(transport).ReadContainer()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped responding")
}
