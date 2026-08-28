package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/mvisonneau/openshuttercounter/internal/logs"
	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// List reports the cameras currently attached.
func List(ctx context.Context, cmd *cli.Command) error {
	reg := registry(logs.LoggerFromContext(ctx), false)

	var (
		devices []usb.DeviceInfo
		err     error
	)

	if cmd.Bool("all") {
		devices, err = usb.List()
	} else {
		devices, err = usb.ListVendors(reg.VendorIDs()...)
	}

	if err != nil {
		return fmt.Errorf("enumerating USB devices: %w", err)
	}

	if cmd.Bool("json") {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		return encoder.Encode(devices)
	}

	if len(devices) == 0 {
		fmt.Println("\nNo camera found.")
		fmt.Println()
		fmt.Println(noDeviceHint)

		return nil
	}

	fmt.Println()

	for _, d := range devices {
		fmt.Printf("  %04x:%04x  %-32s %s\n", d.VendorID, d.ProductID, describeDevice(reg, d), d.Path)
	}

	fmt.Println()

	return nil
}

// describeDevice names a device as helpfully as it can: what the camera calls
// itself, else what the registry knows it as, else an honest shrug.
func describeDevice(reg *manufacturers.Registry, d usb.DeviceInfo) string {
	if d.Product != "" {
		return d.Product
	}

	reader, err := reg.For(d)
	if err != nil {
		return "unrecognised device"
	}

	for _, m := range reader.Models() {
		if m.ProductID == d.ProductID {
			return reader.Name() + " " + m.Name
		}
	}

	return reader.Name() + " camera"
}

// Models lists the cameras this build knows how to read.
func Models(ctx context.Context, cmd *cli.Command) error {
	reg := registry(logs.LoggerFromContext(ctx), false)

	type manufacturerOutput struct {
		Manufacturer string                `json:"manufacturer"`
		VendorID     string                `json:"vendor_id"`
		Models       []manufacturers.Model `json:"models"`
	}

	out := make([]manufacturerOutput, 0, len(reg.Readers()))
	for _, reader := range reg.Readers() {
		out = append(out, manufacturerOutput{
			Manufacturer: reader.Name(),
			VendorID:     fmt.Sprintf("%04x", reader.VendorID()),
			Models:       reader.Models(),
		})
	}

	if cmd.Bool("json") {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		return encoder.Encode(out)
	}

	for _, m := range out {
		fmt.Printf("\n%s  (USB vendor %s)\n\n", m.Manufacturer, m.VendorID)

		for _, model := range m.Models {
			status := "not confirmed by anyone yet"
			if model.Confirmed {
				status = "confirmed"
				if model.ConfirmedOn != "" {
					status += " on " + model.ConfirmedOn
				}
			}

			fmt.Printf("  %-20s USB %s:%04x  (%s)\n", model.Name, m.VendorID, model.ProductID, status)

			for _, c := range model.Counters {
				fmt.Printf("      %-24s %s\n", c.Location, c.Label)
			}
		}
	}

	fmt.Print(modelsFooter)

	return nil
}

const modelsFooter = `
  Older bodies need no entry here: they still answer the standard PTP shutter
  counter property, which is tried first on every camera.

  A camera missing from this list may well work anyway. Run it, and whether it
  worked or not, a trace makes the answer useful to everyone else:

    openshuttercounter --trace mycamera.json

  CONTRIBUTING.md explains what to do with it.

`
