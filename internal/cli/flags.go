package cli

import (
	"github.com/urfave/cli/v3"
)

var deviceFlag = &cli.StringFlag{
	Name:    "device",
	Aliases: []string{"d"},
	Sources: cli.EnvVars("OPENSHUTTERCOUNTER_DEVICE"),
	Usage:   "which camera to talk to: a device `path`, a bus:address pair or a vendor:product ID",
}

var jsonFlag = &cli.BoolFlag{
	Name:    "json",
	Sources: cli.EnvVars("OPENSHUTTERCOUNTER_JSON"),
	Usage:   "emit machine readable output",
}

var rawFlag = &cli.BoolFlag{
	Name:  "raw",
	Usage: "also show the raw replies, which is what a new camera's layout is worked out from",
}

var traceFlag = &cli.StringFlag{
	Name:    "trace",
	Aliases: []string{"t"},
	Sources: cli.EnvVars("OPENSHUTTERCOUNTER_TRACE"),
	Usage:   "record everything exchanged with the camera to `file`, to attach to a report",
}

var noteFlag = &cli.StringFlag{
	Name:  "note",
	Usage: "a `note` stored in the trace, such as how many frames were taken since the last one",
}

var factoryOnlyFlag = &cli.BoolFlag{
	Name:  "factory-only",
	Usage: "skip the standard shutter counter property and use the maker specific channel",
}

var allDevicesFlag = &cli.BoolFlag{
	Name:  "all",
	Usage: "list every USB device, not only cameras from known manufacturers",
}

var asciiFlag = &cli.BoolFlag{
	Name:  "ascii",
	Usage: "show data payloads as text alongside the hex",
}

var onlyFlag = &cli.StringFlag{
	Name:  "only",
	Usage: "restrict the transcript to a comma separated list of hex `opcodes`, e.g. 9052,9053",
}

// readFlags are shared by the root action and the read command, so that
// `openshuttercounter --trace x.json` and `openshuttercounter read --trace
// x.json` behave identically.
var readFlags = []cli.Flag{
	deviceFlag,
	jsonFlag,
	rawFlag,
	traceFlag,
	noteFlag,
	factoryOnlyFlag,
}
