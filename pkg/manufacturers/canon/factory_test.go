package canon

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The payloads below are the 0x9052 data phases captured on an EOS 5D Mark IV
// running firmware 1.2.1, while a working vendor tool read the camera. If
// MarshalBinary stops reproducing them byte for byte the framing has drifted,
// and a malformed payload to 0x9052 puts the body into Err 70.
//
// Do not adjust these to match the code. They are the ground truth.
const (
	capturedMonOpen = "4d6f6e4f70656e00" +
		"00000000" +
		"000000000000000000000000000000000000000000000000"

	capturedMonClose = "4d6f6e436c6f736500" +
		"00000000" +
		"000000000000000000000000000000000000000000000000"

	capturedMonRead100B = "4d6f6e52656164416e644765744461746100" +
		"03000000" +
		"0200000002000000000000000000000000000000" +
		"020000000b1000000000000000000000000000000" +
		"20000000a00000000000000000000000000000000000000"
)

func TestCommandMarshalMatchesCapture(t *testing.T) {
	for _, tc := range []struct {
		name     string
		command  Command
		captured string
	}{
		{"MonOpen", Command{Name: CmdMonOpen}, capturedMonOpen},
		{"MonClose", Command{Name: CmdMonClose}, capturedMonClose},
		{"MonReadAndGetData 0x100b", MonRead(0x100b, 10), capturedMonRead100B},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.command.MarshalBinary()
			require.NoError(t, err)
			assert.Equal(t, tc.captured, hex.EncodeToString(got))
		})
	}
}

// TestArgumentTagsRoundTrip covers the argument encoding that is not an
// integer: FA_GetProperty tags its records with the ASCII code "CSC" instead
// of the integer 2, so the record's first word is a type tag rather than a
// constant.
func TestArgumentTagsRoundTrip(t *testing.T) {
	arg := Argument{Tag: 0x00435343, Value: 0x02000001}
	assert.Equal(t, "CSC=0x2000001", arg.String())
	assert.Equal(t, "0x100b", Int(0x100b).String())

	cmd := Command{Name: "FA_GetProperty", Args: []Argument{arg, {Tag: 0x00435343}}}

	raw, err := cmd.MarshalBinary()
	require.NoError(t, err)

	parsed, err := ParseCommand(raw)
	require.NoError(t, err)
	assert.Equal(t, cmd, parsed)
}

func TestCommandMarshalLengths(t *testing.T) {
	// Name plus NUL, the argc word, twenty bytes per argument, then a
	// trailer of twenty four bytes with no arguments and four with any.
	noArgs, err := Command{Name: CmdMonOpen}.MarshalBinary()
	require.NoError(t, err)
	assert.Len(t, noArgs, len(CmdMonOpen)+1+4+24)

	withArgs, err := MonRead(0x1001, 10).MarshalBinary()
	require.NoError(t, err)
	assert.Len(t, withArgs, len(CmdMonReadAndGetData)+1+4+3*20+4)
}

func TestCommandRefusesWrites(t *testing.T) {
	for name := range refusedCommands {
		t.Run(name, func(t *testing.T) {
			_, err := Command{Name: name}.MarshalBinary()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to send")
		})
	}
}

func TestCommandRejectsMalformedNames(t *testing.T) {
	for _, name := range []string{"", "Mon\x00Open", "Mon Öpen", "Mon\nOpen"} {
		_, err := Command{Name: name}.MarshalBinary()
		assert.Error(t, err, "name %q should be rejected", name)
	}
}
