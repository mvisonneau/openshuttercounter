package cmd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

type readOutput struct {
	Device   deviceOutput    `json:"device"`
	Reading  readingOutput   `json:"reading"`
	Counters []counterOutput `json:"counters"`
}

type deviceOutput struct {
	VendorID  string `json:"vendor_id"`
	ProductID string `json:"product_id"`
	Product   string `json:"product,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Path      string `json:"path"`
}

type readingOutput struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Confirmed    bool   `json:"confirmed"`
	Method       string `json:"method"`
	RatedLife    uint32 `json:"rated_life,omitempty"`
}

type counterOutput struct {
	Label    string `json:"label"`
	Location string `json:"location,omitempty"`
	Value    uint32 `json:"value"`
	Raw      string `json:"raw,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Read reports the actuation counters of the connected camera.
func Read(ctx context.Context, cmd *cli.Command) error {
	session, err := openSession(ctx, cmd)
	if err != nil {
		return err
	}

	defer session.close()

	reading, readErr := session.reader.Read(ctx, session.device, session.info)

	// The trace is worth keeping even when the read failed. A trace of a
	// camera that did not answer is exactly what makes a bug report useful.
	if err := session.saveTrace(ctx); err != nil {
		return err
	}

	if readErr != nil {
		return readErr
	}

	return render(cmd, buildOutput(session.info, reading, cmd.Bool("raw")))
}

func buildOutput(info usb.DeviceInfo, reading manufacturers.Reading, withRaw bool) readOutput {
	out := readOutput{
		Device: deviceOutput{
			VendorID:  fmt.Sprintf("%04x", info.VendorID),
			ProductID: fmt.Sprintf("%04x", info.ProductID),
			Product:   info.Product,
			Serial:    info.Serial,
			Path:      info.Path,
		},
		Reading: readingOutput{
			Manufacturer: reading.Manufacturer,
			Model:        reading.Model,
			Confirmed:    reading.Confirmed,
			Method:       reading.Method,
			RatedLife:    reading.RatedLife,
		},
		Counters: make([]counterOutput, 0, len(reading.Counters)),
	}

	for _, c := range reading.Counters {
		counter := counterOutput{
			Label:    c.Label,
			Location: c.Location,
			Value:    c.Value,
		}

		if withRaw && len(c.Raw) > 0 {
			counter.Raw = hex.EncodeToString(c.Raw)
		}

		if c.Err != nil {
			counter.Error = c.Err.Error()
		}

		out.Counters = append(out.Counters, counter)
	}

	return out
}

func render(cmd *cli.Command, out readOutput) error {
	if cmd.Bool("json") {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")

		return encoder.Encode(out)
	}

	fmt.Printf("\n  %s %s  (USB %s:%s)\n\n",
		out.Reading.Manufacturer, out.Reading.Model, out.Device.VendorID, out.Device.ProductID)

	for _, c := range out.Counters {
		value := humanInt(c.Value)
		if c.Error != "" {
			value = "unavailable"
		}

		where := ""
		if c.Location != "" {
			where = "   (" + c.Location + ")"
		}

		fmt.Printf("  %-22s %12s%s\n", c.Label, value, where)

		if c.Raw != "" {
			fmt.Printf("  %-22s raw: %s\n", "", spacedHex(c.Raw))
		}

		if c.Error != "" {
			fmt.Printf("  %-22s %s\n", "", c.Error)
		}
	}

	if len(out.Counters) > 0 && out.Reading.RatedLife > 0 && out.Counters[0].Error == "" {
		used := float64(out.Counters[0].Value) / float64(out.Reading.RatedLife) * 100

		fmt.Printf("\n  Rated life %s actuations - %.1f%% used\n", humanInt(out.Reading.RatedLife), used)
	}

	if !out.Reading.Confirmed {
		fmt.Print(unconfirmedNotice)
	}

	fmt.Println()

	return nil
}

const unconfirmedNotice = `
  Nobody has confirmed these numbers on this camera yet, so treat them as a
  guess. Confirming takes about ten minutes and needs nothing but the camera:

    openshuttercounter --trace before.json
    # take an exact number of frames, say 20
    openshuttercounter --trace after.json --note "20 frames between traces"

  If every counter moved by exactly the number of frames you took, that is a
  confirmed camera. Send both files along and it goes in the supported list.
  CONTRIBUTING.md has the details.
`

// spacedHex makes a raw reply readable.
func spacedHex(s string) string {
	out := make([]byte, 0, len(s)+len(s)/2)

	for i := 0; i < len(s); i += 2 {
		if i > 0 {
			out = append(out, ' ')
		}

		out = append(out, s[i], s[i+1])
	}

	return string(out)
}
