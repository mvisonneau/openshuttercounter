// Package sony reads the shutter count of Sony Alpha cameras.
//
// Sony bodies do not expose the count as a PTP property, and Sony's Camera
// Remote SDK has no call for it. What they do is stamp it into every image
// they write, inside an enciphered maker note block (EXIF tag 0x9050). So the
// reader asks the camera, over standard read only PTP object operations, for
// the head of the newest image on the card and decodes the count from that.
//
// The figure is therefore the count as of the newest image on the card. Take
// a fresh frame before reading if the number has to be current.
package sony

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/mvisonneau/openshuttercounter/pkg/manufacturers"
	"github.com/mvisonneau/openshuttercounter/pkg/ptp"
	"github.com/mvisonneau/openshuttercounter/pkg/usb"
)

// VendorID is Sony Corporation's USB vendor ID.
const VendorID uint16 = 0x054c

// MethodImageMakerNote names how a Sony reading is obtained.
const MethodImageMakerNote = "sony-image-makernote-0x9050"

const (
	// headLen is how much of an image is fetched. The EXIF block of a
	// Sony JPEG, ARW or HEIF sits well inside this.
	headLen = 512 * 1024

	// maxImagesFetched bounds how many images are opened looking for one
	// that records the count. Only bodies that skip it for some frames
	// need more than one.
	maxImagesFetched = 8

	// maxImagesScanned bounds how many images are described while looking
	// for the newest one. Each description is a round trip of about 50 ms,
	// so a full card would otherwise take minutes. An Alpha 7 IV lists its
	// objects oldest first, so the newest image is among the last few
	// handles; scanning a few dozen also covers RAW+JPEG pairs and a body
	// that orders handles less strictly.
	maxImagesScanned = 32

	// formatAssociation is a folder.
	formatAssociation uint16 = 0x3001
)

var (
	// ErrNoImage is returned when the camera exposes no image to read.
	ErrNoImage = errors.New("the camera exposed no image to read the count from")

	// ErrUnsupportedModel is returned for a Sony body whose maker note
	// layout is not decoded.
	ErrUnsupportedModel = errors.New("unsupported Sony model")
)

// modeHint explains which USB mode works, since it is the commonest failure.
const modeHint = `On the camera, set Menu > Setup > USB > USB Connection Mode to
"Image Transfer (MTP)", or pick that option when the camera asks on connect.
"Image Transfer (MSC)" presents the card as a disk and speaks no PTP at all,
and "Remote Shooting" does not show the card's contents. There must also be
at least one photo on the card: take one first so the count is current.`

// Reader reads Sony Alpha cameras.
type Reader struct {
	logger *zap.Logger
}

// Option configures a Reader.
type Option func(*Reader)

// WithLogger attaches a logger.
func WithLogger(l *zap.Logger) Option {
	return func(r *Reader) {
		if l != nil {
			r.logger = l
		}
	}
}

// New builds a Sony reader.
func New(opts ...Option) *Reader {
	r := &Reader{logger: zap.NewNop()}

	for _, o := range opts {
		o(r)
	}

	return r
}

// Name implements manufacturers.Reader.
func (r *Reader) Name() string { return "Sony" }

// VendorID implements manufacturers.Reader.
func (r *Reader) VendorID() uint16 { return VendorID }

// Models implements manufacturers.Reader.
func (r *Reader) Models() []manufacturers.Model {
	out := make([]manufacturers.Model, 0, len(models))
	for _, m := range models {
		out = append(out, manufacturers.Model{
			Name:        m.Name + " (" + m.ModelID + ")",
			Tried:       m.Tried,
			ProductID:   m.ProductID,
			Confirmed:   m.Confirmed,
			ConfirmedOn: m.ConfirmedOn,
			RatedLife:   m.RatedLife,
			Counters: []manufacturers.CounterSpec{
				{Label: countLabel(m.Layout), Location: fmt.Sprintf("EXIF 0x9050 +%#02x (%s)", m.Layout.CountOffset, m.Layout.Name)},
			},
		})
	}

	return out
}

// Read implements manufacturers.Reader.
func (r *Reader) Read(_ context.Context, device *ptp.Device, info usb.DeviceInfo) (manufacturers.Reading, error) {
	di, err := device.GetDeviceInfo()
	if err != nil {
		return manufacturers.Reading{}, err
	}

	r.logger.Debug("device info",
		zap.String("model", di.Model),
		zap.String("version", di.DeviceVersion),
		zap.String("vendor_extension", fmt.Sprintf("%#x %s", di.VendorExtensionID, di.VendorExtensionDesc)),
		zap.Int("operations", len(di.OperationsSupported)),
	)

	// Each Sony generation keeps the count at its own offset in the 0x9050
	// block. Decoding an unlisted body with this layout would print a
	// plausible number that is wrong, so it is refused instead.
	model := lookupModel(di.Model, info.ProductID)
	if model.ModelID == "" {
		return manufacturers.Reading{}, fmt.Errorf(
			"%w: %q stores its count in a maker note layout this build does not decode yet; "+
				"run with --trace and open an issue, see CONTRIBUTING.md", ErrUnsupportedModel, di.Model)
	}

	if !di.Supports(ptp.OpGetObjectHandles) || !di.Supports(ptp.OpGetObjectInfo) {
		return manufacturers.Reading{}, fmt.Errorf("the camera is not offering its card over PTP\n%s", modeHint)
	}

	images, err := r.newestImages(device)
	if err != nil {
		return manufacturers.Reading{}, err
	}

	obj, frame, err := r.firstCount(device, di, model, images)
	if err != nil {
		return manufacturers.Reading{}, err
	}

	name := model.Name

	label := countLabel(model.Layout)

	where := obj.Filename
	if frame.Shutter != ShutterUnknown {
		where += ", " + frame.Shutter.String() + " shutter"
	}

	return manufacturers.Reading{
		Manufacturer: r.Name(),
		Model:        name,
		Confirmed:    model.Confirmed,
		Method:       MethodImageMakerNote,
		RatedLife:    model.RatedLife,
		Counters: []manufacturers.Counter{{
			Label:    label,
			Location: fmt.Sprintf("%s, taken %s", where, readableDate(obj.CaptureDate)),
			Value:    frame.ShutterCount,
			Raw:      frame.Raw,
		}},
	}, nil
}

// image is one still image on the card.
type image struct {
	handle uint32
	info   ptp.ObjectInfo
}

// firstCount reads the newest image that records the count.
//
// On most bodies that is simply the newest image. On those whose layout is
// guarded, an electronic shutter frame records nothing, so older images are
// tried until one does, and the reading names the one it came from.
func (r *Reader) firstCount(
	device *ptp.Device, di ptp.DeviceInfo, model Model, images []image,
) (ptp.ObjectInfo, FrameCount, error) {
	skipped := 0

	for i, img := range images {
		if i >= maxImagesFetched {
			break
		}

		head, err := r.fetchHead(device, di, img.handle, img.info)
		if err != nil {
			return ptp.ObjectInfo{}, FrameCount{}, err
		}

		frame, err := ParseFrameCount(head, model.Layout)
		if errors.Is(err, ErrNoCountInFrame) {
			r.logger.Debug("image does not record the count, trying an older one",
				zap.String("file", img.info.Filename))

			skipped++

			continue
		}

		if err != nil {
			return ptp.ObjectInfo{}, FrameCount{}, fmt.Errorf("reading %s: %w", img.info.Filename, err)
		}

		if skipped > 0 {
			r.logger.Warn("the newest images do not record the count, so an older one was used",
				zap.Int("skipped", skipped), zap.String("file", img.info.Filename))
		}

		return img.info, frame, nil
	}

	return ptp.ObjectInfo{}, FrameCount{}, fmt.Errorf(
		"%w: none of the %d newest images do. The %s records its count only in images "+
			"taken with the mechanical shutter; take one frame with Shutter Type set to "+
			"Mechanical and read again", ErrNoCountInFrame, skipped, model.Name)
}

// newestImages lists recent still images, newest first.
//
// Handles are not promised to be in creation order, so the last
// maxImagesScanned images are sorted by capture date. PTP dates are
// YYYYMMDDThhmmss, which sorts as text.
func (r *Reader) newestImages(device *ptp.Device) ([]image, error) {
	storages, err := device.GetStorageIDs()
	if err != nil {
		// In Remote Shooting mode the body answers PTP but keeps the
		// card to itself, which is what StoreNotAvailable means here.
		if errors.Is(err, &ptp.ResponseError{Code: ptp.RCStoreNotAvailable}) {
			return nil, fmt.Errorf("the camera is not offering its card: %w\n%s", err, modeHint)
		}

		return nil, err
	}

	var images []image

	for _, storage := range storages {
		handles, err := device.GetObjectHandles(storage, ptp.AllFormats, ptp.RootAndBelow)
		if err != nil {
			r.logger.Debug("listing objects", zap.Uint32("storage", storage), zap.Error(err))

			continue
		}

		r.logger.Debug("storage", zap.String("id", fmt.Sprintf("%#x", storage)), zap.Int("objects", len(handles)))

		for i := len(handles) - 1; i >= 0 && len(images) < maxImagesScanned; i-- {
			obj, err := device.GetObjectInfo(handles[i])
			if err != nil {
				r.logger.Debug("describing object", zap.Uint32("handle", handles[i]), zap.Error(err))

				continue
			}

			if obj.Format == formatAssociation || !isImage(obj.Filename) {
				continue
			}

			images = append(images, image{handle: handles[i], info: obj})
		}
	}

	if len(images) == 0 {
		return nil, fmt.Errorf("%w\n%s", ErrNoImage, modeHint)
	}

	// Stable, so images with the same timestamp keep the newest-handle-
	// first order they were listed in.
	slices.SortStableFunc(images, func(a, b image) int {
		return strings.Compare(b.info.CaptureDate, a.info.CaptureDate)
	})

	r.logger.Debug("newest image",
		zap.String("file", images[0].info.Filename), zap.String("captured", images[0].info.CaptureDate))

	return images, nil
}

// fetchHead reads the start of an image, falling back to the whole file on a
// camera that does not offer partial reads.
func (r *Reader) fetchHead(device *ptp.Device, di ptp.DeviceInfo, handle uint32, obj ptp.ObjectInfo) ([]byte, error) {
	if di.Supports(ptp.OpGetPartialObject) {
		head, err := device.GetPartialObject(handle, 0, headLen)
		if err == nil {
			return head, nil
		}

		r.logger.Debug("partial read failed, fetching the whole image", zap.Error(err))
	}

	r.logger.Info("fetching the whole image, which may take a moment",
		zap.String("file", obj.Filename), zap.Uint32("bytes", obj.CompressedSize))

	return device.GetObject(handle)
}

// readableDate turns a PTP date, YYYYMMDDThhmmss with optional fractions and
// zone, into something a person reads at a glance. Anything else is returned
// as it came.
func readableDate(d string) string {
	if len(d) < 15 || d[8] != 'T' {
		return d
	}

	t, err := time.Parse("20060102T150405", d[:15])
	if err != nil {
		return d
	}

	return t.Format("2006-01-02 15:04:05")
}

func countLabel(l Layout) string {
	if l.MechanicalOnly {
		return "Mechanical shutter count"
	}

	return "Shutter count"
}

func isImage(name string) bool {
	switch strings.ToUpper(path.Ext(name)) {
	case ".JPG", ".JPEG", ".ARW", ".HIF", ".HEIF":
		return true
	default:
		return false
	}
}
