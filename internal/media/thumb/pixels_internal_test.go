package thumb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// claiming is a small real file whose header says it is w by h, which is all a
// decoder needs to try to allocate that many pixels.
func claimingPNG(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// The IHDR chunk is the first: 8 bytes of signature, 4 of length, 4 of
	// type, then width and height, and its CRC over type and data after.
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

func claimingJPEG(t *testing.T, w, h uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewYCbCr(image.Rect(0, 0, 8, 8), image.YCbCrSubsampleRatio420), nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	sof := bytes.Index(b, []byte{0xFF, 0xC0})
	if sof < 0 {
		t.Fatal("no SOF0 in the encoded JPEG")
	}
	binary.BigEndian.PutUint16(b[sof+5:], h)
	binary.BigEndian.PutUint16(b[sof+7:], w)
	return b
}

// TestAPictureTooBigToDecodeIsRefusedFromItsHeader is #558: the pixels are read
// off the header before anything is allocated, and too many is a verdict --
// which is what keeps a restart from trying the same file again.
func TestAPictureTooBigToDecodeIsRefusedFromItsHeader(t *testing.T) {
	t.Parallel()
	th := &Service{}
	for name, body := range map[string][]byte{
		"bomb.png":  claimingPNG(t, 20_000, 20_000),
		"200mp.jpg": claimingJPEG(t, 16_320, 12_240),
	} {
		_, err := th.decode(t.Context(), db.File{Path: name}, bytes.NewReader(body), thumbMedium)
		// "would take" is the refusal from the header, as against the decoder
		// failing on bytes that do not hold what the header claims.
		if !errors.Is(err, ErrNoThumbnail) || !strings.Contains(err.Error(), "would take") {
			t.Errorf("%s = %v, want refused before it is decoded", name, err)
		}
	}
}

// TestAnOrdinaryPhotographIsStillDecodedInGo: the bound is about memory, and a
// 48 MP phone photograph fits it.
func TestAnOrdinaryPhotographIsStillDecodedInGo(t *testing.T) {
	t.Parallel()
	cost, ok := decodedCost(bytes.NewReader(claimingJPEG(t, 8_064, 6_048)))
	if !ok || cost > decodeBudget {
		t.Errorf("a 48 MP JPEG would cost %d MB, over the budget", cost>>20)
	}
	gray, ok := decodedCost(bytes.NewReader(claimingPNG(t, 100, 100)))
	if !ok || gray != 100*100*4 {
		t.Errorf("a 100x100 NRGBA PNG costs %d, want four bytes a pixel", gray)
	}

	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 40, 30))
	img.Set(1, 1, color.White)
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	pic, err := (&Service{}).decode(t.Context(), db.File{Path: "small.png"}, bytes.NewReader(buf.Bytes()), thumbMedium)
	if err != nil || pic.src.Bounds().Dx() != 40 {
		t.Errorf("a small PNG = %v, %v; want it decoded", pic.src, err)
	}
}
