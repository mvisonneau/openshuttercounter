package ptp

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

const (
	// defaultReadChunk is the size of a single bulk IN request. usbfs caps a
	// synchronous bulk transfer at 16 KiB, so asking for more would fail on
	// Linux; larger payloads simply take several reads.
	defaultReadChunk = 16 * 1024

	defaultReadTimeout  = 10 * time.Second
	defaultWriteTimeout = 5 * time.Second

	// maxEmptyReads bounds how many zero length packets in a row are
	// tolerated while waiting for the rest of a container.
	maxEmptyReads = 4
)

// Direction says which way a container travelled.
type Direction string

// The two directions a container can travel.
const (
	// DirectionOut is host to camera.
	DirectionOut Direction = "out"

	// DirectionIn is camera to host.
	DirectionIn Direction = "in"
)

// Recorder is handed every container a Device exchanges, in order.
//
// This is what makes a session reproducible for somebody who does not have the
// camera: a recording of a real conversation can be replayed, decoded and
// compared without any USB capture tooling.
type Recorder interface {
	Record(direction Direction, c Container)
}

// Device drives one PTP conversation over a claimed USB interface.
//
// PTP is strictly serial: one transaction at a time, each identified by an
// incrementing transaction ID. Device is not safe for concurrent use.
type Device struct {
	transport usb.Device
	logger    *zap.Logger

	recorder Recorder

	transactionID uint32
	sessionOpen   bool

	// pending holds bytes that arrived in the same bulk transfer as the
	// container we just returned. In practice a responder terminates each
	// container with a short packet and this stays empty, but keeping the
	// remainder means a chatty device cannot desynchronise us.
	pending []byte

	readChunk    int
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// Option configures a Device.
type Option func(*Device)

// WithLogger attaches a logger. Every container is logged at debug level,
// which is the fastest way to compare our traffic against a capture.
func WithLogger(l *zap.Logger) Option {
	return func(d *Device) {
		if l != nil {
			d.logger = l
		}
	}
}

// WithRecorder records every container exchanged.
func WithRecorder(r Recorder) Option {
	return func(d *Device) { d.recorder = r }
}

// WithTimeouts overrides the bulk transfer timeouts.
func WithTimeouts(read, write time.Duration) Option {
	return func(d *Device) {
		if read > 0 {
			d.readTimeout = read
		}

		if write > 0 {
			d.writeTimeout = write
		}
	}
}

// New wraps a claimed USB device in a PTP conversation.
func New(transport usb.Device, opts ...Option) *Device {
	d := &Device{
		transport:    transport,
		logger:       zap.NewNop(),
		readChunk:    defaultReadChunk,
		readTimeout:  defaultReadTimeout,
		writeTimeout: defaultWriteTimeout,
	}

	for _, o := range opts {
		o(d)
	}

	return d
}

// Response is the outcome of a transaction: the responder's code, its
// parameters, and any data phase that preceded it.
type Response struct {
	Code   uint16
	Params []uint32
	Data   []byte
}

// OK reports whether the responder returned Response_OK.
func (r Response) OK() bool { return r.Code == RCOK }

// Err returns a non nil error unless the responder returned Response_OK.
func (r Response) Err() error {
	if r.OK() {
		return nil
	}

	return &ResponseError{Code: r.Code}
}

// ResponseError is a non OK PTP response code.
type ResponseError struct {
	Code uint16
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("ptp response %#04x %s", e.Code, ResponseName(e.Code))
}

// Is lets callers match a specific response code with errors.Is.
func (e *ResponseError) Is(target error) bool {
	other, ok := target.(*ResponseError)

	return ok && other.Code == e.Code
}

// NextTransactionID reserves the next transaction ID.
func (d *Device) NextTransactionID() uint32 {
	d.transactionID++

	return d.transactionID
}

// SendContainer writes a single container to the bulk OUT endpoint.
func (d *Device) SendContainer(c Container) error {
	raw, err := c.MarshalBinary()
	if err != nil {
		return err
	}

	d.logger.Debug("ptp: send",
		zap.String("type", containerTypeName(c.Type)),
		zap.String("code", fmt.Sprintf("%#04x", c.Code)),
		zap.Uint32("transaction", c.TransactionID),
		zap.Int("payload", len(c.Payload)),
	)

	if d.recorder != nil {
		d.recorder.Record(DirectionOut, c)
	}

	for sent := 0; sent < len(raw); {
		n, err := d.transport.Write(raw[sent:], d.writeTimeout)
		if err != nil {
			return fmt.Errorf("writing %s container %#04x: %w", containerTypeName(c.Type), c.Code, err)
		}

		if n == 0 {
			return fmt.Errorf("writing %s container %#04x: transport accepted no bytes", containerTypeName(c.Type), c.Code)
		}

		sent += n
	}

	return nil
}

// ReadContainer reads one whole container from the bulk IN endpoint.
//
// A responder ends a transfer with a short packet, so buffers are requested
// large and the actual size comes back from the transport. Sizing a read to
// the bytes still outstanding overruns the endpoint and yields EIO.
func (d *Device) ReadContainer() (Container, error) {
	buf, err := d.fill(HeaderLen)
	if err != nil {
		return Container{}, err
	}

	c, length, err := ParseHeader(buf)
	if err != nil {
		return Container{}, err
	}

	if buf, err = d.fill(length); err != nil {
		return Container{}, fmt.Errorf("reading %d byte container: %w", length, err)
	}

	c.Payload = append([]byte(nil), buf[HeaderLen:length]...)
	d.pending = append([]byte(nil), buf[length:]...)

	d.logger.Debug("ptp: recv",
		zap.String("type", containerTypeName(c.Type)),
		zap.String("code", fmt.Sprintf("%#04x", c.Code)),
		zap.Uint32("transaction", c.TransactionID),
		zap.Int("payload", len(c.Payload)),
	)

	if d.recorder != nil {
		d.recorder.Record(DirectionIn, c)
	}

	return c, nil
}

// Transact runs an operation whose data phase, if any, flows from the device.
func (d *Device) Transact(opcode uint16, params ...uint32) (Response, error) {
	return d.transact(Container{
		Type:          ContainerCommand,
		Code:          opcode,
		TransactionID: d.NextTransactionID(),
		Payload:       MarshalParams(params...),
	}, nil)
}

// TransactOut runs an operation with a data phase flowing to the device.
//
// The data container repeats the command's opcode and transaction ID, which is
// what the camera matches it against.
func (d *Device) TransactOut(opcode uint16, data []byte, params ...uint32) (Response, error) {
	return d.transact(Container{
		Type:          ContainerCommand,
		Code:          opcode,
		TransactionID: d.NextTransactionID(),
		Payload:       MarshalParams(params...),
	}, data)
}

// OpenSession opens PTP session 1. A session that is already open is not an
// error: the camera may have been left in one by a previous run.
func (d *Device) OpenSession() error {
	resp, err := d.Transact(OpOpenSession, 1)
	if err != nil {
		return err
	}

	if !resp.OK() && resp.Code != RCSessionAlreadyOpen {
		return fmt.Errorf("opening session: %w", resp.Err())
	}

	d.sessionOpen = true

	return nil
}

// CloseSession closes the PTP session if one is open.
func (d *Device) CloseSession() error {
	if !d.sessionOpen {
		return nil
	}

	d.sessionOpen = false

	if _, err := d.Transact(OpCloseSession); err != nil {
		return err
	}

	return nil
}

// ClearHalt clears a stall on the bulk endpoints.
func (d *Device) ClearHalt() error {
	return d.transport.ClearHalt()
}

// Info returns the identity of the device being driven.
func (d *Device) Info() usb.DeviceInfo {
	return d.transport.Info()
}

// fill reads until at least n bytes are buffered.
func (d *Device) fill(n int) ([]byte, error) {
	chunk := make([]byte, d.readChunk)

	empty := 0

	for len(d.pending) < n {
		read, err := d.transport.Read(chunk, d.readTimeout)
		if err != nil {
			return nil, fmt.Errorf("bulk read: %w", err)
		}

		if read == 0 {
			// A zero length packet is how a device terminates a transfer
			// whose payload happened to be an exact multiple of the
			// endpoint's packet size. Seeing one mid container is normal;
			// seeing only those means the camera has stopped talking.
			empty++
			if empty > maxEmptyReads {
				return nil, fmt.Errorf(
					"camera stopped responding with %d of %d bytes read", len(d.pending), n,
				)
			}

			continue
		}

		empty = 0

		d.pending = append(d.pending, chunk[:read]...)
	}

	return d.pending, nil
}

func (d *Device) transact(command Container, out []byte) (Response, error) {
	if err := d.SendContainer(command); err != nil {
		return Response{}, err
	}

	if out != nil {
		if err := d.SendContainer(Container{
			Type:          ContainerData,
			Code:          command.Code,
			TransactionID: command.TransactionID,
			Payload:       out,
		}); err != nil {
			return Response{}, err
		}
	}

	var resp Response

	for {
		c, err := d.ReadContainer()
		if err != nil {
			return Response{}, err
		}

		switch c.Type {
		case ContainerData:
			resp.Data = append(resp.Data, c.Payload...)
		case ContainerResponse:
			resp.Code = c.Code
			resp.Params = c.Params()

			return resp, nil
		case ContainerEvent:
			// Asynchronous events can interleave with a transaction. They
			// are not our business here, so drop them and keep reading.
			d.logger.Debug("ptp: dropping asynchronous event", zap.String("code", fmt.Sprintf("%#04x", c.Code)))
		default:
			return Response{}, fmt.Errorf("unexpected container type %#04x in transaction %d", c.Type, command.TransactionID)
		}
	}
}

// ContainerTypeName names a container type for humans.
func ContainerTypeName(t uint16) string { return containerTypeName(t) }

func containerTypeName(t uint16) string {
	switch t {
	case ContainerCommand:
		return "command"
	case ContainerData:
		return "data"
	case ContainerResponse:
		return "response"
	case ContainerEvent:
		return "event"
	default:
		return fmt.Sprintf("unknown(%#04x)", t)
	}
}
