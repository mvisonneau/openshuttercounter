package pcap

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
)

// The captures this decoder was built against are not distributed, so the
// tests build their own: real PTP containers, wrapped in each of the capture
// formats the decoder claims to read. That exercises the part that can
// actually go wrong, which is the framing rather than the payloads.

// monReadPayload is a MonReadAndGetData command as it goes on the wire.
const monReadPayload = "4d6f6e52656164416e644765744461746100" +
	"03000000" +
	"0200000002000000000000000000000000000000" +
	"020000000b1000000000000000000000000000000" +
	"20000000a00000000000000000000000000000000000000"

// conversation is a short but realistic exchange: a command with parameters,
// a data phase carrying a factory command, its response, then the read back.
func conversation(t *testing.T) [][]byte {
	t.Helper()

	payload, err := hex.DecodeString(monReadPayload)
	require.NoError(t, err)

	containers := []ptp.Container{
		{Type: ptp.ContainerCommand, Code: 0x9052, TransactionID: 11, Payload: ptp.MarshalParams(0, 1)},
		{Type: ptp.ContainerData, Code: 0x9052, TransactionID: 11, Payload: payload},
		{Type: ptp.ContainerResponse, Code: ptp.RCOK, TransactionID: 11},
		{Type: ptp.ContainerCommand, Code: 0x9053, TransactionID: 12, Payload: ptp.MarshalParams(0, 0, 1)},
		// Data and response containers routinely carry a different code
		// from the command they belong to, which is the wrinkle the
		// decoder has to handle.
		{Type: ptp.ContainerData, Code: 0x0000, TransactionID: 12, Payload: []byte{0x00, 0x66, 0, 0, 0, 0, 0, 0, 0x10, 0x00}},
		{Type: ptp.ContainerResponse, Code: ptp.RCOK, TransactionID: 12},
	}

	out := make([][]byte, 0, len(containers))

	for _, c := range containers {
		raw, err := c.MarshalBinary()
		require.NoError(t, err)

		out = append(out, raw)
	}

	return out
}

// u32 narrows a length for a capture header. The test data is small, so a
// value that does not fit is a bug in the test rather than a runtime concern.
func u32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic("test capture length out of range")
	}

	return uint32(n) //nolint:gosec // bounded immediately above
}

// usbpcapPacket wraps a USB payload in the pseudo-header USBPcap prepends,
// which carries its own header length and payload length.
func usbpcapPacket(payload []byte) []byte {
	const headerLen = 27

	pkt := make([]byte, headerLen+len(payload))
	binary.LittleEndian.PutUint16(pkt[0:2], headerLen)
	binary.LittleEndian.PutUint32(pkt[23:27], u32(len(payload)))
	copy(pkt[headerLen:], payload)

	return pkt
}

// usbmonPacket wraps a payload in a Linux usbmon mmapped header, which is a
// fixed 64 bytes the decoder simply skips.
func usbmonPacket(payload []byte) []byte {
	const headerLen = 64

	return append(make([]byte, headerLen), payload...)
}

// buildPcapng assembles a minimal but valid pcapng file.
func buildPcapng(linkType uint16, packets [][]byte) []byte {
	var out bytes.Buffer

	block := func(blockType uint32, body []byte) {
		for len(body)%4 != 0 {
			body = append(body, 0)
		}

		total := u32(12 + len(body))

		header := make([]byte, 8)
		binary.LittleEndian.PutUint32(header[0:4], blockType)
		binary.LittleEndian.PutUint32(header[4:8], total)

		trailer := make([]byte, 4)
		binary.LittleEndian.PutUint32(trailer, total)

		out.Write(header)
		out.Write(body)
		out.Write(trailer)
	}

	// Section header: byte order magic, version, unspecified section length.
	shb := make([]byte, 16)
	binary.LittleEndian.PutUint32(shb[0:4], 0x1a2b3c4d)
	binary.LittleEndian.PutUint16(shb[4:6], 1)
	binary.LittleEndian.PutUint64(shb[8:16], ^uint64(0))
	block(0x0a0d0d0a, shb)

	// Interface description: the link type everything below is in.
	idb := make([]byte, 8)
	binary.LittleEndian.PutUint16(idb[0:2], linkType)
	binary.LittleEndian.PutUint32(idb[4:8], 0xffff)
	block(0x00000001, idb)

	for _, p := range packets {
		epb := make([]byte, 20+len(p))
		binary.LittleEndian.PutUint32(epb[12:16], u32(len(p)))
		binary.LittleEndian.PutUint32(epb[16:20], u32(len(p)))
		copy(epb[20:], p)
		block(0x00000006, epb)
	}

	return out.Bytes()
}

// buildPcap assembles a minimal but valid classic libpcap file.
func buildPcap(linkType uint16, packets [][]byte) []byte {
	global := make([]byte, 24)
	binary.LittleEndian.PutUint32(global[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(global[4:6], 2)
	binary.LittleEndian.PutUint16(global[6:8], 4)
	binary.LittleEndian.PutUint32(global[16:20], 0xffff)
	binary.LittleEndian.PutUint32(global[20:24], uint32(linkType))

	var out bytes.Buffer

	out.Write(global)

	for _, p := range packets {
		header := make([]byte, 16)
		binary.LittleEndian.PutUint32(header[8:12], u32(len(p)))
		binary.LittleEndian.PutUint32(header[12:16], u32(len(p)))
		out.Write(header)
		out.Write(p)
	}

	return out.Bytes()
}

func TestDecodeReadsEveryCaptureFormat(t *testing.T) {
	containers := conversation(t)

	usbpcap := make([][]byte, 0, len(containers))
	usbmon := make([][]byte, 0, len(containers))

	for _, c := range containers {
		usbpcap = append(usbpcap, usbpcapPacket(c))
		usbmon = append(usbmon, usbmonPacket(c))
	}

	for name, raw := range map[string][]byte{
		"pcapng/usbpcap": buildPcapng(linkTypeUSBPcap, usbpcap),
		"pcapng/usbmon":  buildPcapng(linkTypeUSBLinuxMMapped, usbmon),
		"pcap/usbpcap":   buildPcap(linkTypeUSBPcap, usbpcap),
		"pcap/usbmon":    buildPcap(linkTypeUSBLinuxMMapped, usbmon),
	} {
		t.Run(name, func(t *testing.T) {
			records, err := Decode(raw)
			require.NoError(t, err)
			require.Len(t, records, len(containers))

			assert.Equal(t, 1, records[0].Index)
			assert.Equal(t, uint16(0x9052), records[0].Container.Code)
			assert.Equal(t, []uint32{0, 1}, records[0].Container.Params())

			assert.Equal(t, monReadPayload, hex.EncodeToString(records[1].Container.Payload))
		})
	}
}

// TestDecodeCarriesOpcodesAcrossTheTransaction covers the wrinkle that made
// early readings of this channel misleading: data and response containers do
// not reliably repeat the command's opcode, so it has to be carried across by
// transaction ID. Reading a data phase without doing that is how the channel
// came to be attributed to the wrong opcode for years.
func TestDecodeCarriesOpcodesAcrossTheTransaction(t *testing.T) {
	containers := conversation(t)

	packets := make([][]byte, 0, len(containers))
	for _, c := range containers {
		packets = append(packets, usbpcapPacket(c))
	}

	records, err := Decode(buildPcapng(linkTypeUSBPcap, packets))
	require.NoError(t, err)
	require.Len(t, records, 6)

	// The data container at index 4 carries code 0x0000 on the wire, but
	// belongs to the 0x9053 command that opened transaction 12.
	assert.Equal(t, uint16(0x0000), records[4].Container.Code)
	assert.Equal(t, uint16(0x9053), records[4].Opcode)

	// Responses are attributed the same way.
	assert.Equal(t, uint16(0x9053), records[5].Opcode)
	assert.Equal(t, ptp.RCOK, records[5].Container.Code)
}

func TestDecodeSkipsPacketsWithoutPTP(t *testing.T) {
	containers := conversation(t)

	packets := make([][]byte, 0, len(containers)+2)
	packets = append(packets, usbpcapPacket(nil), usbpcapPacket([]byte{0x01, 0x02}))

	for _, c := range containers {
		packets = append(packets, usbpcapPacket(c))
	}

	records, err := Decode(buildPcapng(linkTypeUSBPcap, packets))
	require.NoError(t, err)
	assert.Len(t, records, 6)
}

func TestDecodeIgnoresUnknownLinkTypes(t *testing.T) {
	const linkTypeEthernet = 1

	records, err := Decode(buildPcapng(linkTypeEthernet, [][]byte{usbpcapPacket(conversation(t)[0])}))
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestDecodeRejectsNonCaptures(t *testing.T) {
	_, err := Decode([]byte("not a capture at all"))
	require.Error(t, err)

	_, err = Decode(nil)
	require.Error(t, err)
}

func TestDecodeFileReportsMissingFiles(t *testing.T) {
	_, err := DecodeFile("does-not-exist.pcapng")
	require.Error(t, err)
}
