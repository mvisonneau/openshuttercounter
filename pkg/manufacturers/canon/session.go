package canon

import (
	"errors"
	"fmt"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
)

// Session is an open conversation on the factory command channel.
//
// It is not safe for concurrent use, and neither is the camera: the MPU
// monitor is opened, read and closed as one sequence.
type Session struct {
	dev  *ptp.Device
	open bool
}

// NewSession wraps an already opened PTP session.
func NewSession(dev *ptp.Device) *Session {
	return &Session{dev: dev}
}

// Open starts the MPU monitor.
func (s *Session) Open() error {
	if err := s.Send(Command{Name: CmdMonOpen}, false); err != nil {
		return fmt.Errorf("opening the MPU monitor: %w", err)
	}

	s.open = true

	return nil
}

// Close stops the MPU monitor. Closing a session that was never opened is not
// an error, so this is safe to defer.
func (s *Session) Close() error {
	if !s.open {
		return nil
	}

	s.open = false

	if err := s.Send(Command{Name: CmdMonClose}, false); err != nil {
		return fmt.Errorf("closing the MPU monitor: %w", err)
	}

	return nil
}

// Send issues a command. expectReply selects the second PTP parameter, which
// tells the camera whether a 0x9053 read will follow.
func (s *Session) Send(cmd Command, expectReply bool) error {
	payload, err := cmd.MarshalBinary()
	if err != nil {
		return err
	}

	reply := uint32(0)
	if expectReply {
		reply = 1
	}

	resp, err := s.dev.TransactOut(OpFactorySend, payload, 0, reply)
	if err != nil {
		return fmt.Errorf("sending %s: %w", cmd.Name, err)
	}

	if err := resp.Err(); err != nil {
		return fmt.Errorf("sending %s: %w", cmd.Name, err)
	}

	return nil
}

// Receive reads the reply to the last command.
func (s *Session) Receive() ([]byte, error) {
	resp, err := s.dev.Transact(OpFactoryRecv, 0, 0, 1)
	if err != nil {
		return nil, fmt.Errorf("reading the factory channel reply: %w", err)
	}

	if err := resp.Err(); err != nil {
		return nil, fmt.Errorf("reading the factory channel reply: %w", err)
	}

	return resp.Data, nil
}

// ReadMPU reads length bytes of MPU memory at address.
func (s *Session) ReadMPU(address, length uint32) ([]byte, error) {
	if !s.open {
		return nil, errors.New("the MPU monitor is not open")
	}

	if err := s.Send(MonRead(address, length), true); err != nil {
		return nil, err
	}

	reply, err := s.Receive()
	if err != nil {
		return nil, fmt.Errorf("reading MPU %#04x: %w", address, err)
	}

	return reply, nil
}
