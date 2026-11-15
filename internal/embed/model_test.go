package embed_test

import (
	"context"
	"encoding/json"
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

// TestTheModelGivesWhatPythonGave runs both towers through the real helper and
// compares them with what onnxruntime gave in Python for the same picture and
// query. The bundle is hundreds of megabytes, so this runs where
// STRATUS_TEST_EMBED_MODEL names one and stratus-embed is on the PATH.
func TestTheModelGivesWhatPythonGave(t *testing.T) {
	dir := os.Getenv("STRATUS_TEST_EMBED_MODEL")
	helper := embed.LookupHelper()
	if dir == "" || helper == "" {
		t.Skip("STRATUS_TEST_EMBED_MODEL is not set or stratus-embed is not installed")
	}
	m, err := embed.Open(dir, helper)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)

	raw, err := os.ReadFile("testdata/siglip2_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Image []float32 `json:"image"`
		Query string    `json:"query"`
		Text  []float32 `json:"text"`
	}
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("testdata/pixels_src.png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	v, err := m.Image(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	// Not equal, and not close to equal either: the helper's minimal build
	// rounds the int8 vision tower differently from the Python wheel, which
	// moved fifty photographs to between 0.988 and 1 of Python's vectors while
	// leaving the spike's mAP exactly where it was (#505). Anything broken in
	// between -- the pixels, their order, the framing -- lands far below this.
	if c := cosine(v, want.Image); c < 0.98 {
		t.Errorf("the picture's vector is %.5f from Python's", c)
	}
	q, err := m.Text(ctx, want.Query)
	if err != nil {
		t.Fatal(err)
	}
	if c := cosine(q, want.Text); c < 0.999 {
		t.Errorf("the query's vector is %.5f from Python's", c)
	}
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
