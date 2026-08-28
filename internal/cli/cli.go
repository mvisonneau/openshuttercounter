package cli

import (
	"github.com/urfave/cli/v3"

	"github.com/mvisonneau/openshuttercounter/internal/app"
	"github.com/mvisonneau/openshuttercounter/internal/cmd"
)

// NewApp configures the CLI application.
func NewApp() *cli.Command {
	return &cli.Command{
		Name:                  app.Name,
		Version:               app.Version,
		Usage:                 "read the shutter actuation count of a camera",
		HideHelpCommand:       true,
		EnableShellCompletion: true,

		Description: `Reads the shutter and mirror actuation counters straight off the camera over
USB. Cameras that answer the standard PTP counter property are read that way;
those that do not are read through their manufacturer's own channel.

Running it with no command reads the connected camera.

If your camera is not recognised, running with --trace produces a file that is
enough for somebody else to confirm support for it. See CONTRIBUTING.md.

This tool only ever reads. It will not send any command that writes to the
camera, and refuses to build one; see docs/PROTOCOL.md for why that matters.`,

		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:    "log-level",
				Sources: cli.EnvVars("OPENSHUTTERCOUNTER_LOG_LEVEL"),
				Usage:   "log `level` (debug,info,warn,error,fatal,panic)",
				Value:   "info",
			},
			&cli.StringFlag{
				Name:    "log-format",
				Sources: cli.EnvVars("OPENSHUTTERCOUNTER_LOG_FORMAT"),
				Usage:   "log `format` (json,text)",
				Value:   "text",
			},
		}, readFlags...),

		Action: cmd.Execute(cmd.Read),

		Commands: []*cli.Command{
			{
				Name:   "read",
				Usage:  "read the actuation counters of the connected camera",
				Action: cmd.Execute(cmd.Read),
				Flags:  readFlags,
			},
			{
				Name:   "list",
				Usage:  "list the cameras currently connected",
				Action: cmd.Execute(cmd.List),
				Flags: []cli.Flag{
					allDevicesFlag,
					jsonFlag,
				},
			},
			{
				Name:   "models",
				Usage:  "list the cameras this build knows how to read",
				Action: cmd.Execute(cmd.Models),
				Flags: []cli.Flag{
					jsonFlag,
				},
			},
			{
				Name:      "decode",
				Usage:     "print the conversation held in a trace file",
				ArgsUsage: "<trace.json>",
				Description: `Prints the PTP conversation a trace file contains, with each manufacturer's
commands decoded.

Traces written by openshuttercounter itself are the usual input. USB captures
in pcap or pcapng format are read too, for anyone who already has one.

Comparing two traces taken a known number of frames apart is how a camera gets
confirmed, and how an unrecognised one gets worked out.`,
				Action: cmd.Execute(cmd.Decode),
				Flags: []cli.Flag{
					asciiFlag,
					onlyFlag,
				},
			},
		},
	}
}
