package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers/canon"
	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/trace"
)

// opcodeNames annotates the transcript. Only the opcodes that matter here are
// named; anything else is printed as a bare number.
var opcodeNames = map[uint16]string{
	0x1001: "GetDeviceInfo",
	0x1002: "OpenSession",
	0x1003: "CloseSession",
	0x1014: "GetDevicePropDesc",
	0x1015: "GetDevicePropValue",
	0x1016: "SetDevicePropValue",
	0x9052: "CanonFactorySend",
	0x9053: "CanonFactoryRecv",
	0x9108: "EOS_GetDeviceInfoEx",
	0x9114: "EOS_SetRemoteMode",
	0x9115: "EOS_SetEventMode",
	0x9116: "EOS_GetEvent",
	0x911b: "EOS_SetUILock",
	0x911c: "EOS_ResetUILock",
}

// Decode prints the PTP conversation held in a trace file.
func Decode(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return errors.New("expected exactly one trace file")
	}

	only, err := parseOpcodeFilter(cmd.String("only"))
	if err != nil {
		return err
	}

	filtering := len(only) > 0

	file, err := trace.ReadFile(cmd.Args().First())
	if err != nil {
		return err
	}

	printTraceHeader(file)

	fmt.Printf("\n%5s  %-4s %-6s %-18s %-6s %s\n", "#", "dir", "type", "opcode", "txid", "detail")
	fmt.Println(strings.Repeat("-", 100))

	shown := 0

	for _, e := range file.Entries {
		if filtering && !only[e.Opcode] {
			continue
		}

		shown++

		fmt.Printf("%5d  %-4s %-6s %-18s %-6d %s\n",
			e.Index,
			e.Direction,
			containerTypeName(e.Type),
			opcodeLabel(e.Opcode),
			e.Transaction,
			detail(e, cmd.Bool("ascii")),
		)
	}

	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%d containers in the trace, %d shown\n\n", len(file.Entries), shown)

	if len(file.Entries) == 0 {
		fmt.Println("Nothing decoded. If this was a USB capture, check that it covers the")
		fmt.Println("right root hub and was taken while the camera was actually being read.")
	}

	return nil
}

// printTraceHeader shows what the trace is of, which is most of what makes one
// useful to somebody who was not there when it was recorded.
func printTraceHeader(file trace.File) {
	fmt.Println()

	if file.Tool != "" {
		version := file.ToolVersion
		if version != "" {
			version = " " + version
		}

		fmt.Printf("  recorded by   %s%s\n", file.Tool, version)
	}

	if !file.RecordedAt.IsZero() {
		fmt.Printf("  recorded at   %s\n", file.RecordedAt.Format("2006-01-02 15:04:05 MST"))
	}

	if file.Host.OS != "" {
		fmt.Printf("  host          %s/%s\n", file.Host.OS, file.Host.Arch)
	}

	if file.Device.VendorID != 0 || file.Device.Product != "" {
		name := file.Device.Product
		if name == "" {
			name = "unnamed device"
		}

		fmt.Printf("  camera        %s (USB %04x:%04x)\n", name, file.Device.VendorID, file.Device.ProductID)
	}

	if file.Notes != "" {
		fmt.Printf("  notes         %s\n", file.Notes)
	}
}

// detail renders whatever is most useful about a container: the parameters of
// a command, the response name, and for a maker specific channel the command
// the data phase actually carries.
func detail(e trace.Entry, ascii bool) string {
	payload, err := e.Bytes()
	if err != nil {
		return "(undecodable payload)"
	}

	container, err := e.Container()
	if err != nil {
		return "(undecodable payload)"
	}

	switch e.Type {
	case ptp.ContainerCommand:
		params := container.Params()
		if len(params) == 0 {
			return ""
		}

		out := make([]string, len(params))
		for i, p := range params {
			out[i] = fmt.Sprintf("%#x", p)
		}

		return "params=" + strings.Join(out, ", ")

	case ptp.ContainerResponse:
		return ptp.ResponseName(e.Code)

	case ptp.ContainerData:
		if e.Opcode == canon.OpFactorySend {
			if command, cmdErr := canon.ParseCommand(payload); cmdErr == nil {
				return command.String() + "  " + renderPayload(payload, ascii)
			}
		}

		return renderPayload(payload, ascii)

	default:
		return renderPayload(payload, ascii)
	}
}

func renderPayload(b []byte, ascii bool) string {
	if len(b) == 0 {
		return ""
	}

	const preview = 32

	shown := b
	suffix := ""

	if len(shown) > preview {
		shown, suffix = shown[:preview], " ..."
	}

	hexed := make([]string, len(shown))
	for i, c := range shown {
		hexed[i] = fmt.Sprintf("%02x", c)
	}

	out := strings.Join(hexed, " ") + suffix

	if ascii {
		printable := make([]byte, len(shown))
		for i, c := range shown {
			if c >= 0x20 && c < 0x7f {
				printable[i] = c
			} else {
				printable[i] = '.'
			}
		}

		out = string(printable) + suffix + "  |  " + out
	}

	return out
}

func opcodeLabel(opcode uint16) string {
	if name, ok := opcodeNames[opcode]; ok {
		return name
	}

	return fmt.Sprintf("%#04x", opcode)
}

func containerTypeName(t uint16) string {
	switch t {
	case ptp.ContainerCommand:
		return "cmd"
	case ptp.ContainerData:
		return "data"
	case ptp.ContainerResponse:
		return "resp"
	case ptp.ContainerEvent:
		return "event"
	default:
		return "?"
	}
}

func parseOpcodeFilter(spec string) (map[uint16]bool, error) {
	filter := map[uint16]bool{}

	if spec == "" {
		return filter, nil
	}

	for field := range strings.SplitSeq(spec, ",") {
		field = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(field), "0x"))

		value, err := strconv.ParseUint(field, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("--only: %q is not a hexadecimal opcode: %w", field, err)
		}

		filter[uint16(value)] = true
	}

	return filter, nil
}
