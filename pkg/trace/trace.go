// Package trace records and reads back a PTP conversation with a camera.
//
// A trace is what turns "it printed some numbers on my body" into something
// anyone else can check. It holds every container exchanged, so a reading can
// be decoded, compared against a second trace taken a known number of frames
// later, and confirmed without the camera being anywhere nearby.
//
// Traces are written by openshuttercounter itself, so contributing support for
// a new camera needs no capture tooling. USB captures in pcap or pcapng format
// are read as well, for anyone who already has one.
package trace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/mvisonneau/openshuttercounter/pkg/pcap"
	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// FormatVersion is the trace file schema version. It goes in every file so a
// trace recorded today can still be read once the format has moved on.
const FormatVersion = 1

// Entry is one container, in the order it was exchanged.
type Entry struct {
	// Index is the entry's position in the conversation, starting at one.
	Index int `json:"index"`

	// At is when the container was exchanged. Absent in entries read from a
	// USB capture, which carries its own timestamps in a different form.
	At *time.Time `json:"at,omitempty"`

	// Direction is "out" for host to camera, "in" for camera to host.
	Direction ptp.Direction `json:"direction"`

	// Type is the container type: command, data, response or event.
	Type uint16 `json:"type"`

	// Code is the container's own code: an opcode on a command, a response
	// code on a response.
	Code uint16 `json:"code"`

	// Opcode is the operation the container belongs to. Data and response
	// containers do not reliably repeat the command's opcode, so it is
	// carried across by transaction ID.
	Opcode uint16 `json:"opcode"`

	Transaction uint32 `json:"transaction"`

	// Payload is the container's payload, hex encoded.
	Payload string `json:"payload,omitempty"`
}

// Bytes returns the entry's decoded payload.
func (e Entry) Bytes() ([]byte, error) {
	if e.Payload == "" {
		return nil, nil
	}

	return hex.DecodeString(e.Payload)
}

// Container rebuilds the PTP container the entry describes.
func (e Entry) Container() (ptp.Container, error) {
	payload, err := e.Bytes()
	if err != nil {
		return ptp.Container{}, fmt.Errorf("entry %d has an undecodable payload: %w", e.Index, err)
	}

	return ptp.Container{
		Type:          e.Type,
		Code:          e.Code,
		TransactionID: e.Transaction,
		Payload:       payload,
	}, nil
}

// Host describes the machine a trace was recorded on, which matters because
// the three USB backends are quite different from one another.
type Host struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// File is a recorded conversation.
type File struct {
	Tool          string `json:"tool"`
	ToolVersion   string `json:"tool_version,omitempty"`
	FormatVersion int    `json:"format_version"`

	RecordedAt time.Time `json:"recorded_at,omitzero"`

	Host Host `json:"host"`

	// Device is the camera the conversation was with.
	Device usb.DeviceInfo `json:"device"`

	// Notes is free text the person recording chose to add, such as how
	// many frames were taken since the previous trace.
	Notes string `json:"notes,omitempty"`

	Entries []Entry `json:"entries"`
}

// Recorder collects containers as they are exchanged and writes them out.
//
// It implements ptp.Recorder, so attaching one to a device is all that is
// needed:
//
//	recorder := trace.NewRecorder(version)
//	device := ptp.New(transport, ptp.WithRecorder(recorder))
//	...
//	recorder.WriteFile(path)
type Recorder struct {
	mu sync.Mutex

	file    File
	opcodes map[uint32]uint16
	now     func() time.Time
}

// NewRecorder starts a recording.
func NewRecorder(toolVersion string) *Recorder {
	return &Recorder{
		file: File{
			Tool:          "openshuttercounter",
			ToolVersion:   toolVersion,
			FormatVersion: FormatVersion,
			Host:          Host{OS: runtime.GOOS, Arch: runtime.GOARCH},
		},
		opcodes: map[uint32]uint16{},
		now:     time.Now,
	}
}

// Describe records which camera the conversation is with.
func (r *Recorder) Describe(info usb.DeviceInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.file.Device = info
}

// Annotate attaches a free text note, such as the number of frames taken.
func (r *Recorder) Annotate(notes string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.file.Notes = notes
}

// Record implements ptp.Recorder.
func (r *Recorder) Record(direction ptp.Direction, c ptp.Container) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Data and response containers do not reliably carry the opcode of the
	// operation they belong to, so it is remembered from the command.
	opcode := c.Code
	if c.Type == ptp.ContainerCommand {
		r.opcodes[c.TransactionID] = c.Code
	} else if known, ok := r.opcodes[c.TransactionID]; ok {
		opcode = known
	}

	at := r.now().UTC()

	r.file.Entries = append(r.file.Entries, Entry{
		Index:       len(r.file.Entries) + 1,
		At:          &at,
		Direction:   direction,
		Type:        c.Type,
		Code:        c.Code,
		Opcode:      opcode,
		Transaction: c.TransactionID,
		Payload:     hex.EncodeToString(c.Payload),
	})
}

// File returns the recording so far.
func (r *Recorder) File() File {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.file
	out.Entries = append([]Entry(nil), r.file.Entries...)

	return out
}

// Write emits the recording as JSON.
func (r *Recorder) Write(w io.Writer) error {
	file := r.File()
	file.RecordedAt = r.now().UTC()

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(file); err != nil {
		return fmt.Errorf("writing trace: %w", err)
	}

	return nil
}

// WriteFile saves the recording.
func (r *Recorder) WriteFile(path string) error {
	f, err := os.Create(path) //nolint:gosec // writing the file the user named is the point
	if err != nil {
		return fmt.Errorf("creating trace file %s: %w", path, err)
	}

	defer f.Close() //nolint:errcheck // the meaningful close is the one below

	if err := r.Write(f); err != nil {
		return err
	}

	return f.Close()
}

// ReadFile reads a trace back.
//
// Both openshuttercounter's own format and USB captures in pcap or pcapng
// format are accepted, so a contributor can send whichever they have.
func ReadFile(path string) (File, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // reading the file the user named is the point
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}

	return Read(raw)
}

// Read parses a trace from bytes, detecting which format it is in.
func Read(raw []byte) (File, error) {
	if looksLikeJSON(raw) {
		var file File

		if err := json.Unmarshal(raw, &file); err != nil {
			return File{}, fmt.Errorf("parsing trace: %w", err)
		}

		if file.FormatVersion > FormatVersion {
			return File{}, fmt.Errorf(
				"trace is format version %d and this build understands up to %d, so please upgrade",
				file.FormatVersion, FormatVersion,
			)
		}

		return file, nil
	}

	return fromCapture(raw)
}

// fromCapture converts a USB capture into the same shape.
func fromCapture(raw []byte) (File, error) {
	records, err := pcap.Decode(raw)
	if err != nil {
		return File{}, err
	}

	file := File{
		Tool:          "usb-capture",
		FormatVersion: FormatVersion,
		Entries:       make([]Entry, 0, len(records)),
	}

	for _, r := range records {
		// A capture does not label direction in a form worth trusting, so
		// it is inferred from the container type: commands and the data
		// phases that follow them go out, responses and events come back.
		direction := ptp.DirectionIn
		if r.Container.Type == ptp.ContainerCommand {
			direction = ptp.DirectionOut
		}

		file.Entries = append(file.Entries, Entry{
			Index:       r.Index,
			Direction:   direction,
			Type:        r.Container.Type,
			Code:        r.Container.Code,
			Opcode:      r.Opcode,
			Transaction: r.Container.TransactionID,
			Payload:     hex.EncodeToString(r.Container.Payload),
		})
	}

	return file, nil
}

// looksLikeJSON reports whether the bytes look like one of our trace files.
func looksLikeJSON(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}

	return false
}
