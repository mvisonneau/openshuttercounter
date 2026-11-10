package sony

import "strings"

// Model is what is known about one Sony body.
type Model struct {
	// Name is the marketing name; ModelID is what the body reports in its
	// PTP DeviceInfo and EXIF, for example "ILCE-7M4".
	Name    string
	ModelID string

	// ProductID is the USB product ID in MTP mode. Sony bodies enumerate
	// with a different product ID in each USB connection mode (an Alpha 7
	// IV is 0x0da6 in MTP and 0x0da7 in Remote Shooting), so the model is
	// matched on ModelID first and this is only a fallback.
	ProductID uint16

	// Layout is where this body keeps its count in the 0x9050 block.
	Layout Layout

	RatedLife   uint32
	Confirmed   bool
	ConfirmedOn string

	// Tried records a body read over USB and checked against ExifTool on
	// the same image, short of the frame count confirmation.
	Tried bool
}

// models lists bodies whose 0x9050 block layout is decoded. The layout for
// each comes from ExifTool's Sony module; the bodies with a USB product ID
// have been read over USB and checked against ExifTool on the same image, and
// the rest are supported but untried.
//
// Left out on purpose: bodies on ExifTool's Tag9050a layout (Alpha 7, 7R, 7S,
// 7 II, 6000 and older), whose count ExifTool marks as uncertain and as
// wrapping at 65536 on some models; and bodies with no mechanical shutter
// (ZV-E10, ZV-E10 II, ZV-E1, FX30), whose field counts images rather than
// actuations, if it is there at all.
var models = []Model{
	{Name: "Alpha 6100", ModelID: "ILCE-6100", Layout: Layout9050b},
	{Name: "Alpha 6100", ModelID: "ILCE-6100A", Layout: Layout9050b},
	{Name: "Alpha 6300", ModelID: "ILCE-6300", Layout: Layout9050b},
	{Name: "Alpha 6400", ModelID: "ILCE-6400", Layout: Layout9050b},
	{Name: "Alpha 6400", ModelID: "ILCE-6400A", Layout: Layout9050b},
	{Name: "Alpha 6500", ModelID: "ILCE-6500", Layout: Layout9050b},
	{Name: "Alpha 6600", ModelID: "ILCE-6600", Layout: Layout9050b},
	{Name: "Alpha 7C", ModelID: "ILCE-7C", Layout: Layout9050b},
	{Name: "Alpha 7 III", ModelID: "ILCE-7M3", Layout: Layout9050b},
	{Name: "Alpha 7R II", ModelID: "ILCE-7RM2", Layout: Layout9050b},
	{Name: "Alpha 7R III", ModelID: "ILCE-7RM3", Layout: Layout9050b},
	{Name: "Alpha 7R III", ModelID: "ILCE-7RM3A", Layout: Layout9050b},
	{Name: "Alpha 7R IV", ModelID: "ILCE-7RM4", Layout: Layout9050b},
	{Name: "Alpha 7R IV", ModelID: "ILCE-7RM4A", Layout: Layout9050b},
	{Name: "Alpha 7S II", ModelID: "ILCE-7SM2", Layout: Layout9050b},
	{Name: "Alpha 9", ModelID: "ILCE-9", Layout: Layout9050b},
	{Name: "Alpha 9 II", ModelID: "ILCE-9M2", Layout: Layout9050b},
	{Name: "Alpha 99 II", ModelID: "ILCA-99M2", Layout: Layout9050b},

	{Name: "Alpha 7 IV", ModelID: "ILCE-7M4", ProductID: 0x0da6, Layout: Layout9050c, Tried: true},
	{Name: "Alpha 1", ModelID: "ILCE-1", Layout: Layout9050c},
	{Name: "Alpha 7R V", ModelID: "ILCE-7RM5", Layout: Layout9050c},
	{Name: "Alpha 7S III", ModelID: "ILCE-7SM3", Layout: Layout9050c},
	{Name: "FX3", ModelID: "ILME-FX3", Layout: Layout9050c},
	{Name: "FX3", ModelID: "ILME-FX3A", Layout: Layout9050c},

	{Name: "Alpha 7C II", ModelID: "ILCE-7CM2", ProductID: 0x0e8b, Layout: Layout9050d, Tried: true},
	{Name: "Alpha 7CR", ModelID: "ILCE-7CR", Layout: Layout9050d},
	{Name: "Alpha 6700", ModelID: "ILCE-6700", Layout: Layout9050d},
	{Name: "FX2", ModelID: "ILME-FX2", Layout: Layout9050d},

	// Confirmed: 25 mechanical frames in a row read 450 to 474, one each.
	{
		Name: "Alpha 1 II", ModelID: "ILCE-1M2", ProductID: 0x0eeb, Layout: Layout9050dMechanical,
		Confirmed: true, ConfirmedOn: "firmware 4.0", Tried: true,
	},
	{Name: "Alpha 7 V", ModelID: "ILCE-7M5", Layout: Layout9050dMechanical},
}

// lookupModel matches what the camera calls itself, then the product ID.
func lookupModel(modelID string, productID uint16) Model {
	for _, m := range models {
		if strings.EqualFold(m.ModelID, strings.TrimSpace(modelID)) {
			return m
		}
	}

	for _, m := range models {
		if m.ProductID != 0 && m.ProductID == productID {
			return m
		}
	}

	return Model{}
}
