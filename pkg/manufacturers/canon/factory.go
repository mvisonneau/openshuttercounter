// Package canon implements the factory command channel Canon EOS bodies carry
// over two vendor PTP opcodes, and the shutter and mirror actuation counters
// that live behind it.
//
// Up to roughly DIGIC 5, Canon exposed a shutter counter as PTP device
// property 0xD1AC. On DIGIC 6 and newer bodies that property is gone, and no
// amount of searching the property tree finds the counters, because they are
// not stored on the main processor at all: they live in the memory of the MPU,
// the microcontroller that drives the shutter and mirror. Reaching them means
// asking the MPU directly, over a text command channel:
//
//	0x9052  send a command, data-OUT
//	0x9053  read the reply, data-IN
//
// Both require a data phase. Calling either with parameters alone returns
// DeviceBusy, which is what made this channel look closed for years.
//
// docs/PROTOCOL.md documents the wire format and the evidence behind it.
package canon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Vendor opcodes carrying the factory command channel.
const (
	OpFactorySend uint16 = 0x9052
	OpFactoryRecv uint16 = 0x9053
)

// Commands this package sends. All of them are reads.
const (
	// CmdMonOpen opens the MPU monitor. Takes no arguments and returns no
	// reply.
	CmdMonOpen = "MonOpen"

	// CmdMonClose closes it again.
	CmdMonClose = "MonClose"

	// CmdMonReadAndGetData reads MPU memory. Takes (2, address, length).
	CmdMonReadAndGetData = "MonReadAndGetData"
)

// refusedCommands are factory commands that write to the camera. They are
// refused here rather than merely left unimplemented, because this channel is
// the same one commercial service software uses to rewrite calibration data,
// the counters themselves and the whole MPU EEPROM. A mistake at this layer is
// not recoverable with a battery pull.
var refusedCommands = map[string]string{
	"FA_SetProperty": "writes factory properties, including calibration data",
	"FA_SetCalendar": "writes the camera's calendar",
	"MonWrite":       "writes MPU memory, where the actuation counters live",
	"MonCall":        "calls arbitrary MPU code",
}

// Wire format of a 0x9052 payload:
//
//	<command name>       NUL terminated ASCII
//	<uint32 argc>        number of argument records
//	<argc x 20 bytes>    argument records
//	<zero padding>       24 bytes with no arguments, 4 bytes otherwise
//
// An argument record is a uint32 type tag, a uint32 value and twelve zero
// bytes. Two tags have been seen on the wire: the integer 2, and a four
// character ASCII code naming a property, which is how FA_GetProperty passes
// "CSC".
const (
	argRecordLen       = 20
	trailerWithoutArgs = 24
	trailerWithArgs    = 4
)

// ArgTagInteger is the argument tag for a plain integer.
const ArgTagInteger = uint32(2)

// maxArgs bounds what MarshalBinary will build. Nothing observed on the wire
// takes more than three arguments.
const maxArgs = 16

// monReadMode is the constant first argument to MonReadAndGetData. It is 2 in
// every capture taken so far; what it selects is not established.
const monReadMode = uint32(2)

// Argument is one argument record.
type Argument struct {
	// Tag says how to read Value. ArgTagInteger means a plain integer;
	// every other tag observed so far has been a four character ASCII code.
	Tag uint32

	// Value is the argument itself, or the first four bytes of whatever the
	// tag describes.
	Value uint32
}

// Int builds an integer argument.
func Int(v uint32) Argument {
	return Argument{Tag: ArgTagInteger, Value: v}
}

// String renders an argument the way the capture decoder prints it.
func (a Argument) String() string {
	if a.Tag == ArgTagInteger {
		return fmt.Sprintf("%#x", a.Value)
	}

	tag := make([]byte, 4)
	binary.LittleEndian.PutUint32(tag, a.Tag)

	name := strings.TrimRight(string(tag), "\x00")
	for i := range len(name) {
		if name[i] < 0x20 || name[i] > 0x7e {
			return fmt.Sprintf("%#x=%#x", a.Tag, a.Value)
		}
	}

	return fmt.Sprintf("%s=%#x", name, a.Value)
}

// Command is one factory channel command.
type Command struct {
	Name string
	Args []Argument
}

// MarshalBinary renders the command exactly as the captured vendor traffic
// does. The byte for byte match is asserted by the package tests against
// payloads taken off the wire; malformed payloads to 0x9052 put the camera
// into Err 70, so this is not a place to improvise.
func (c Command) MarshalBinary() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}

	trailer := trailerWithArgs
	if len(c.Args) == 0 {
		trailer = trailerWithoutArgs
	}

	out := make([]byte, 0, len(c.Name)+1+4+argRecordLen*len(c.Args)+trailer)

	out = append(out, c.Name...)
	out = append(out, 0)
	//nolint:gosec // validate above bounds the argument count to maxArgs
	out = binary.LittleEndian.AppendUint32(out, uint32(len(c.Args)))

	for _, arg := range c.Args {
		out = binary.LittleEndian.AppendUint32(out, arg.Tag)
		out = binary.LittleEndian.AppendUint32(out, arg.Value)
		out = append(out, make([]byte, argRecordLen-8)...)
	}

	return append(out, make([]byte, trailer)...), nil
}

// MonRead builds the command that reads length bytes of MPU memory at address.
func MonRead(address, length uint32) Command {
	return Command{
		Name: CmdMonReadAndGetData,
		Args: []Argument{Int(monReadMode), Int(address), Int(length)},
	}
}

// ParseCommand reads back a 0x9052 payload.
//
// This is what makes a capture legible: it turns the bytes of a data phase
// into the command and arguments that produced them, which is how a new
// camera's address map gets read out of somebody else's traffic.
func ParseCommand(payload []byte) (Command, error) {
	end := -1

	for i, b := range payload {
		if b == 0 {
			end = i

			break
		}
	}

	if end <= 0 {
		return Command{}, errors.New("payload carries no NUL terminated command name")
	}

	cmd := Command{Name: string(payload[:end])}

	rest := payload[end+1:]
	if len(rest) < 4 {
		return Command{}, errors.New("payload ends before its argument count")
	}

	argc := binary.LittleEndian.Uint32(rest[:4])
	rest = rest[4:]

	if int(argc)*argRecordLen > len(rest) {
		return cmd, fmt.Errorf("payload declares %d arguments but carries %d bytes", argc, len(rest))
	}

	for i := range argc {
		record := rest[i*argRecordLen : (i+1)*argRecordLen]
		cmd.Args = append(cmd.Args, Argument{
			Tag:   binary.LittleEndian.Uint32(record[0:4]),
			Value: binary.LittleEndian.Uint32(record[4:8]),
		})
	}

	return cmd, nil
}

// String renders a command the way the capture decoder prints it.
func (c Command) String() string {
	if len(c.Args) == 0 {
		return c.Name + "()"
	}

	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = a.String()
	}

	return c.Name + "(" + strings.Join(args, ", ") + ")"
}

func (c Command) validate() error {
	if c.Name == "" {
		return errors.New("factory command has no name")
	}

	// The argument count goes on the wire as a uint32, and nothing observed
	// takes more than three arguments. A payload the camera cannot parse is
	// what puts it into Err 70, so an implausible one is refused here.
	if len(c.Args) > maxArgs {
		return fmt.Errorf("factory command %s has %d arguments, more than the %d allowed", c.Name, len(c.Args), maxArgs)
	}

	if reason, refused := refusedCommands[c.Name]; refused {
		return fmt.Errorf("refusing to send %s: it %s", c.Name, reason)
	}

	if strings.ContainsRune(c.Name, 0) {
		return fmt.Errorf("factory command %q contains a NUL", c.Name)
	}

	for i := range len(c.Name) {
		if c.Name[i] < 0x20 || c.Name[i] > 0x7e {
			return fmt.Errorf("factory command %q is not printable ASCII", c.Name)
		}
	}

	return nil
}
