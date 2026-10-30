package canon

import "fmt"

// VendorID is Canon Inc.'s USB vendor ID.
const VendorID uint16 = 0x04a9

// Counter is one value in MPU memory, named and located.
type Counter struct {
	// Label is what the CLI prints.
	Label string `json:"label"`

	// Address is the MPU memory address, as passed to MonReadAndGetData.
	Address uint32 `json:"mpu_address"`

	// Length is how many bytes to read. Every counter observed so far
	// answers in ten.
	Length uint32 `json:"length"`
}

// Model is what is known about one camera body.
type Model struct {
	// Name is the marketing name.
	Name string `json:"name"`

	// ProductID is the USB product ID the body enumerates with.
	ProductID uint16 `json:"product_id"`

	// RatedLife is the manufacturer's shutter durability rating, used only
	// to put the reading in context. Zero means unknown.
	RatedLife uint32 `json:"rated_life,omitempty"`

	// Counters is the MPU address map.
	Counters []Counter `json:"counters"`

	// Confirmed records whether somebody with this body checked the
	// readings against a known number of frames and reported back. An
	// unconfirmed map is a reasonable guess and the CLI says so.
	Confirmed bool `json:"confirmed"`

	// ConfirmedOn describes the firmware it was confirmed against.
	ConfirmedOn string `json:"confirmed_on,omitempty"`
}

// Each address map below was established differentially: read every address,
// take an exact number of frames, read again, and keep whatever advanced by
// exactly that number. Adding a body is the same job, and CONTRIBUTING.md
// walks through it.
//
// On the 5D Mark IV, MPU 0x1198 also answers, in 32 bytes, but is an error log
// rather than a counter: it does not move with actuations. It is left out
// deliberately.
var models = []Model{
	{
		Name:        "EOS 5D Mark IV",
		ProductID:   0x3281,
		RatedLife:   150000,
		Confirmed:   true,
		ConfirmedOn: "firmware 1.2.1",
		Counters: []Counter{
			{Label: "Shutter actuations", Address: 0x1001, Length: 10},
			{Label: "Mirror actuations", Address: 0x100b, Length: 10},
			{Label: "Total counter", Address: 0x1015, Length: 10},
		},
	},
	{
		Name:        "EOS 5DS R",
		ProductID:   0x32af,
		RatedLife:   150000,
		Confirmed:   true,
		ConfirmedOn: "firmware 1.1.4 R",
		Counters: []Counter{
			{Label: "Shutter actuations", Address: 0x1001, Length: 10},
			{Label: "Mirror actuations", Address: 0x100b, Length: 10},
			{Label: "Total counter", Address: 0x1015, Length: 10},
		},
	},
}

// Models returns every body with a known address map.
func Models() []Model {
	out := make([]Model, len(models))
	copy(out, models)

	return out
}

// LookupModel returns the address map for a USB product ID.
func LookupModel(productID uint16) (Model, bool) {
	for _, m := range models {
		if m.ProductID == productID {
			return m, true
		}
	}

	return Model{}, false
}

// FallbackModel is what to try on a body with no entry of its own.
//
// The channel itself is very likely the same across DIGIC 6 and newer bodies,
// but the addresses are not guaranteed to be, so a reading obtained this way
// is a hypothesis until somebody with that body has checked it against a known
// frame count. Trying is harmless either way: MonReadAndGetData only reads.
func FallbackModel(productID uint16) Model {
	reference := models[0]

	return Model{
		Name:      fmt.Sprintf("unrecognised Canon body (USB %04x:%04x)", VendorID, productID),
		ProductID: productID,
		Counters:  reference.Counters,
		Confirmed: false,
	}
}
