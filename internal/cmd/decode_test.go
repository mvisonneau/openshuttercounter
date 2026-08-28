package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOpcodeFilter(t *testing.T) {
	filter, err := parseOpcodeFilter("9052,0x9053, 1015")
	require.NoError(t, err)
	assert.Equal(t, map[uint16]bool{0x9052: true, 0x9053: true, 0x1015: true}, filter)

	filter, err = parseOpcodeFilter("")
	require.NoError(t, err)
	assert.Empty(t, filter)

	_, err = parseOpcodeFilter("nope")
	assert.Error(t, err)
}

func TestOpcodeLabel(t *testing.T) {
	assert.Equal(t, "CanonFactorySend", opcodeLabel(0x9052))
	assert.Equal(t, "CanonFactoryRecv", opcodeLabel(0x9053))
	assert.Equal(t, "0x9999", opcodeLabel(0x9999))
}
