// Package pcap extracts a PTP conversation from a USB capture.
//
// It reads pcap and pcapng files produced by USBPcap on Windows or usbmon on
// Linux, strips the USB transport header and reassembles the PTP container
// stream. This is the tool that recovered the protocol in the first place, and
// it is how the MPU address map gets extended to a new camera body: capture a
// working reader, take a known number of frames, capture again, and diff.
package pcap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
)

// Link types, from the pcap link type registry.
const (
	linkTypeUSBPcap          uint16 = 249
	linkTypeUSBLinux         uint16 = 189
	linkTypeUSBLinuxMMapped  uint16 = 220
	usbLinuxHeaderLen               = 48
	usbLinuxMMappedHeaderLen        = 64
)

// Record is one PTP container found in a capture, in file order.
type Record struct {
	// Index is the container's position in the capture, starting at one.
	Index int

	// Container is the container itself.
	Container ptp.Container

	// Opcode is the operation the container belongs to. Data and response
	// containers do not always repeat the command's opcode, so it is
	// carried across from the command with the same transaction ID.
	Opcode uint16
}

// DecodeFile reads a capture from disk.
func DecodeFile(path string) ([]Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return Decode(raw)
}

// Decode extracts every PTP container from a capture.
func Decode(raw []byte) ([]Record, error) {
	packets, err := readCapture(raw)
	if err != nil {
		return nil, err
	}

	var (
		records []Record
		opcodes = map[uint32]uint16{}
	)

	for _, p := range packets {
		payload := stripUSB(p)
		if len(payload) == 0 {
			continue
		}

		for _, c := range containers(payload) {
			opcode := c.Code

			if c.Type == ptp.ContainerCommand {
				opcodes[c.TransactionID] = c.Code
			} else if known, ok := opcodes[c.TransactionID]; ok {
				opcode = known
			}

			records = append(records, Record{
				Index:     len(records) + 1,
				Container: c,
				Opcode:    opcode,
			})
		}
	}

	return records, nil
}

type packet struct {
	linkType uint16
	data     []byte
}

func readCapture(raw []byte) ([]packet, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("capture is %d bytes, too short to identify", len(raw))
	}

	if binary.BigEndian.Uint32(raw[:4]) == 0x0a0d0d0a {
		return readPcapng(raw)
	}

	return readPcap(raw)
}

// readPcap parses the classic libpcap format.
func readPcap(raw []byte) ([]packet, error) {
	const globalHeaderLen = 24

	if len(raw) < globalHeaderLen {
		return nil, fmt.Errorf("pcap file is %d bytes, shorter than its header", len(raw))
	}

	var order binary.ByteOrder

	switch binary.LittleEndian.Uint32(raw[:4]) {
	case 0xa1b2c3d4, 0xa1b23c4d:
		order = binary.LittleEndian
	case 0xd4c3b2a1, 0x4d3cb2a1:
		order = binary.BigEndian
	default:
		return nil, errors.New("not a pcap or pcapng file")
	}

	rawLinkType := order.Uint32(raw[20:24])
	if rawLinkType > math.MaxUint16 {
		return nil, fmt.Errorf("pcap declares link type %d, which is out of range", rawLinkType)
	}

	linkType := uint16(rawLinkType)

	var packets []packet

	for off := globalHeaderLen; off+16 <= len(raw); {
		captured := int(order.Uint32(raw[off+8 : off+12]))
		off += 16

		if captured < 0 || off+captured > len(raw) {
			break
		}

		packets = append(packets, packet{linkType: linkType, data: raw[off : off+captured]})
		off += captured
	}

	return packets, nil
}

// readPcapng parses the pcapng format, which is what Wireshark writes today.
func readPcapng(raw []byte) ([]packet, error) {
	const (
		blockSectionHeader   uint32 = 0x0a0d0d0a
		blockInterfaceDesc   uint32 = 0x00000001
		blockSimplePacket    uint32 = 0x00000003
		blockEnhancedPacket  uint32 = 0x00000006
		enhancedPacketPreLen        = 20
	)

	var (
		order     binary.ByteOrder = binary.LittleEndian
		linkTypes []uint16
		packets   []packet
	)

	for off := 0; off+12 <= len(raw); {
		blockType := order.Uint32(raw[off : off+4])

		if blockType == blockSectionHeader {
			// The byte order magic tells us how to read this section.
			if binary.LittleEndian.Uint32(raw[off+8:off+12]) == 0x1a2b3c4d {
				order = binary.LittleEndian
			} else {
				order = binary.BigEndian
			}

			linkTypes = nil
		}

		length := int(order.Uint32(raw[off+4 : off+8]))
		if length < 12 || off+length > len(raw) {
			break
		}

		body := raw[off+8 : off+length-4]

		switch blockType {
		case blockInterfaceDesc:
			if len(body) >= 2 {
				linkTypes = append(linkTypes, order.Uint16(body[:2]))
			}
		case blockEnhancedPacket:
			if len(body) >= enhancedPacketPreLen {
				iface := int(order.Uint32(body[:4]))
				captured := int(order.Uint32(body[12:16]))

				if enhancedPacketPreLen+captured <= len(body) {
					packets = append(packets, packet{
						linkType: linkTypeAt(linkTypes, iface),
						data:     body[enhancedPacketPreLen : enhancedPacketPreLen+captured],
					})
				}
			}
		case blockSimplePacket:
			if len(body) >= 4 {
				captured := int(order.Uint32(body[:4]))
				if 4+captured <= len(body) {
					packets = append(packets, packet{
						linkType: linkTypeAt(linkTypes, 0),
						data:     body[4 : 4+captured],
					})
				}
			}
		}

		off += length
	}

	return packets, nil
}

func linkTypeAt(linkTypes []uint16, index int) uint16 {
	if index >= 0 && index < len(linkTypes) {
		return linkTypes[index]
	}

	return 0
}

// stripUSB removes the capture's USB transport header, leaving the bytes the
// device actually exchanged.
func stripUSB(p packet) []byte {
	switch p.linkType {
	case linkTypeUSBPcap:
		if len(p.data) < 27 {
			return nil
		}

		headerLen := int(binary.LittleEndian.Uint16(p.data[:2]))
		dataLen := int(binary.LittleEndian.Uint32(p.data[23:27]))

		if headerLen < 27 || headerLen > len(p.data) {
			return nil
		}

		return p.data[headerLen:min(headerLen+dataLen, len(p.data))]

	case linkTypeUSBLinux, linkTypeUSBLinuxMMapped:
		headerLen := usbLinuxHeaderLen
		if p.linkType == linkTypeUSBLinuxMMapped {
			headerLen = usbLinuxMMappedHeaderLen
		}

		if len(p.data) <= headerLen {
			return nil
		}

		return p.data[headerLen:]

	default:
		return nil
	}
}

// containers pulls the PTP containers out of one USB payload.
func containers(payload []byte) []ptp.Container {
	var out []ptp.Container

	for off := 0; off+ptp.HeaderLen <= len(payload); {
		c, length, err := ptp.ParseHeader(payload[off:])
		if err != nil {
			return out
		}

		if c.Type < ptp.ContainerCommand || c.Type > ptp.ContainerEvent {
			return out
		}

		c.Payload = payload[off+ptp.HeaderLen : min(off+length, len(payload))]
		out = append(out, c)

		if length > len(payload)-off {
			return out
		}

		off += length
	}

	return out
}
