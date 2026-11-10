package sony

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// TIFF and EXIF tags on the way down to Sony's counter.
const (
	tagExifIFD   uint16 = 0x8769
	tagMakerNote uint16 = 0x927c
	tag9050      uint16 = 0x9050
)

// Layout says where one generation of bodies keeps its values inside the
// deciphered 0x9050 block. The names are ExifTool's.
type Layout struct {
	// Name is ExifTool's name for the table, for example "Tag9050c".
	Name string

	// CountOffset locates the count: an int32u of which the low 24 bits
	// are the value.
	CountOffset int

	// ShutterOffset locates three int16u that are all zero when the frame
	// used the electronic shutter. Negative when the layout has no such
	// field.
	ShutterOffset int

	// MechanicalOnly reports whether the count leaves out frames taken
	// with the electronic shutter.
	MechanicalOnly bool

	// GuardZeros, when set, is how many leading bytes of the block must be
	// zero for the layout to apply at all. Bodies that use the layout only
	// for some frames mark those frames this way; any other frame carries
	// no count this build can find.
	GuardZeros int
}

// The layouts decoded so far.
var (
	// Layout9050b is used by bodies from mid 2015 to 2020: the ILCE-6100,
	// 6300, 6400, 6500, 6600, 7C, 7M3, 7RM2, 7RM3, 7RM4, 7SM2, 9, 9M2 and
	// ILCA-99M2. ExifTool places both fields at the same offsets as in
	// Tag9050c, which this project has read on an Alpha 7 IV.
	Layout9050b = Layout{Name: "Tag9050b", CountOffset: 0x3a, ShutterOffset: 0x26}

	// Layout9050c is used by the ILCE-1, 7M4, 7RM5, 7SM3 and FX3.
	Layout9050c = Layout{Name: "Tag9050c", CountOffset: 0x3a, ShutterOffset: 0x26}

	// Layout9050d is used by the ILCE-6700, 7CM2, 7CR and FX2. ExifTool
	// documents the count as mechanical actuations only, and the block
	// has no per-frame shutter field.
	Layout9050d = Layout{Name: "Tag9050d", CountOffset: 0x0a, ShutterOffset: -1, MechanicalOnly: true}

	// Layout9050dMechanical is used by the ILCE-1M2 and 7M5. They write the
	// Tag9050d layout only for frames that used the mechanical shutter,
	// which ExifTool recognises by five leading zero bytes. Electronic
	// shutter frames use a layout nobody has decoded.
	Layout9050dMechanical = Layout{
		Name: "Tag9050d, mechanical frames only", CountOffset: 0x0a, ShutterOffset: -1,
		MechanicalOnly: true, GuardZeros: 5,
	}
)

// Shutter says which shutter made a frame, where the layout records it.
type Shutter int

// The shutter a frame was taken with.
const (
	ShutterUnknown Shutter = iota
	ShutterMechanical
	ShutterElectronic
)

func (s Shutter) String() string {
	switch s {
	case ShutterMechanical:
		return "mechanical"
	case ShutterElectronic:
		return "electronic"
	case ShutterUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

var (
	// ErrNoExif is returned when an image carries no EXIF block.
	ErrNoExif = errors.New("no EXIF data found in the image")

	// ErrNoMakerNote is returned when the EXIF has no Sony maker note, or
	// the maker note has no 0x9050 block.
	ErrNoMakerNote = errors.New("no Sony 0x9050 maker note block found in the image")

	// ErrNoCountInFrame is returned for an image whose block does not use
	// the layout, which on the bodies that guard it means the image was
	// taken with the electronic shutter.
	ErrNoCountInFrame = errors.New("this image does not record the shutter count")
)

// FrameCount is what one image says about the camera that took it.
type FrameCount struct {
	// ShutterCount is the camera's frame counter at the moment the image
	// was taken.
	ShutterCount uint32

	// Shutter is the shutter the image was made with, if the layout says.
	Shutter Shutter

	// Raw is the deciphered 0x9050 block, kept so a reading on a body with
	// a different layout can still be worked out.
	Raw []byte
}

// decipherTable inverts Sony's substitution cipher. A byte b below 249 is
// enciphered as b³ mod 249, which is a bijection on 0..248 because 249 = 3·83
// and 3 is coprime to both 2 and 82; bytes 249-255 are left alone.
var decipherTable = func() [256]byte {
	var t [256]byte

	for b := range 256 {
		if b < 249 {
			t[(b*b*b)%249] = byte(b)
		} else {
			t[b] = byte(b)
		}
	}

	return t
}()

// Decipher reverses the cipher Sony applies to its 0x9xxx maker note blocks.
func Decipher(in []byte) []byte {
	out := make([]byte, len(in))
	for i, c := range in {
		out[i] = decipherTable[c]
	}

	return out
}

// ParseFrameCount extracts the shutter count from the start of an image file.
//
// It accepts a JPEG, or anything that is a TIFF container from its first byte
// (ARW is). For other containers, HEIF among them, it falls back to looking
// for an "Exif\0\0" marker, which is how they embed the same block. Only the
// head of the file is needed: the EXIF block sits in the first few tens of
// kilobytes of every Sony format.
func ParseFrameCount(file []byte, layout Layout) (FrameCount, error) {
	tiff, err := findTIFF(file)
	if err != nil {
		return FrameCount{}, err
	}

	block, err := find9050(tiff)
	if err != nil {
		return FrameCount{}, err
	}

	plain := Decipher(block)

	if layout.GuardZeros > 0 {
		if len(plain) < layout.GuardZeros || !bytes.Equal(plain[:layout.GuardZeros], make([]byte, layout.GuardZeros)) {
			return FrameCount{Raw: plain}, ErrNoCountInFrame
		}
	}

	need := layout.CountOffset + 4
	if layout.ShutterOffset >= 0 {
		need = max(need, layout.ShutterOffset+6)
	}

	if len(plain) < need {
		return FrameCount{}, fmt.Errorf("0x9050 block is %d bytes, too short for %s", len(plain), layout.Name)
	}

	frame := FrameCount{
		ShutterCount: binary.LittleEndian.Uint32(plain[layout.CountOffset:]) & 0x00ffffff,
		Raw:          plain,
	}

	if layout.GuardZeros > 0 {
		frame.Shutter = ShutterMechanical
	}

	if layout.ShutterOffset >= 0 {
		frame.Shutter = ShutterMechanical
		if bytes.Equal(plain[layout.ShutterOffset:layout.ShutterOffset+6], make([]byte, 6)) {
			frame.Shutter = ShutterElectronic
		}
	}

	return frame, nil
}

// findTIFF returns the TIFF structure EXIF lives in, from its header onwards.
func findTIFF(file []byte) ([]byte, error) {
	if isTIFF(file) {
		return file, nil
	}

	if len(file) >= 4 && file[0] == 0xff && file[1] == 0xd8 {
		if tiff, ok := jpegExif(file); ok {
			return tiff, nil
		}
	}

	marker := []byte("Exif\x00\x00")
	for at := 0; ; {
		i := bytes.Index(file[at:], marker)
		if i < 0 {
			return nil, ErrNoExif
		}

		start := at + i + len(marker)
		if isTIFF(file[start:]) {
			return file[start:], nil
		}

		at = start
	}
}

// jpegExif walks JPEG markers to the APP1 segment holding EXIF.
func jpegExif(file []byte) ([]byte, bool) {
	for off := 2; off+4 <= len(file); {
		if file[off] != 0xff {
			return nil, false
		}

		marker := file[off+1]
		if marker == 0xda || marker == 0xd9 { // start of scan, end of image
			return nil, false
		}

		length := int(binary.BigEndian.Uint16(file[off+2:]))
		body := off + 4
		end := off + 2 + length

		if length < 2 || end > len(file) {
			// Truncated head: the segment runs past what was fetched.
			// The EXIF header is still usable if it is in what we have.
			end = len(file)
		}

		if marker == 0xe1 && bytes.HasPrefix(file[body:end], []byte("Exif\x00\x00")) {
			return file[body+6 : end], true
		}

		off = end
	}

	return nil, false
}

func isTIFF(b []byte) bool {
	return len(b) >= 8 && (bytes.HasPrefix(b, []byte("II*\x00")) || bytes.HasPrefix(b, []byte("MM\x00*")))
}

// find9050 follows IFD0 to the EXIF IFD, to the maker note, to its 0x9050
// entry, and returns that entry's still enciphered bytes.
func find9050(tiff []byte) ([]byte, error) {
	order := binary.ByteOrder(binary.LittleEndian)
	if tiff[0] == 'M' {
		order = binary.BigEndian
	}

	ifd0 := order.Uint32(tiff[4:])

	exif, ok := findEntry(tiff, order, ifd0, tagExifIFD)
	if !ok {
		return nil, ErrNoExif
	}

	note, ok := findEntry(tiff, order, order.Uint32(exif.value), tagMakerNote)
	if !ok {
		return nil, ErrNoMakerNote
	}

	// Sony maker notes open with "SONY DSC \0\0\0" (or a similar twelve
	// byte tag) followed by a standard IFD whose offsets, like the rest of
	// the file's, count from the TIFF header. Some older bodies omit the
	// prefix and start straight on the IFD.
	start := note.offset
	if bytes.HasPrefix(tiff[start:], []byte("SONY")) || bytes.HasPrefix(tiff[start:], []byte("VHAB")) {
		start += 12
	}

	//nolint:gosec // start is bounded by len(tiff), checked by findEntry
	block, ok := findEntry(tiff, order, uint32(start), tag9050)
	if !ok {
		return nil, ErrNoMakerNote
	}

	return block.value, nil
}

type entry struct {
	value  []byte
	offset int
}

// findEntry looks a tag up in the IFD at ifd and returns its data.
func findEntry(tiff []byte, order binary.ByteOrder, ifd uint32, tag uint16) (entry, bool) {
	base := int(ifd)
	if base <= 0 || base+2 > len(tiff) {
		return entry{}, false
	}

	n := int(order.Uint16(tiff[base:]))

	for i := range n {
		e := base + 2 + 12*i
		if e+12 > len(tiff) {
			return entry{}, false
		}

		if order.Uint16(tiff[e:]) != tag {
			continue
		}

		size := typeSize(order.Uint16(tiff[e+2:])) * int(order.Uint32(tiff[e+4:]))
		if size <= 4 {
			return entry{value: tiff[e+8 : e+8+size], offset: e + 8}, true
		}

		off := int(order.Uint32(tiff[e+8:]))
		if off < 0 || off+size > len(tiff) {
			return entry{}, false
		}

		return entry{value: tiff[off : off+size], offset: off}, true
	}

	return entry{}, false
}

// typeSize is the byte size of one value of a TIFF field type.
func typeSize(t uint16) int {
	switch t {
	case 1, 2, 6, 7: // byte, ascii, sbyte, undefined
		return 1
	case 3, 8: // short, sshort
		return 2
	case 4, 9, 11, 13: // long, slong, float, ifd
		return 4
	case 5, 10, 12: // rational, srational, double
		return 8
	default:
		return 1
	}
}
