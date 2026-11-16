package embed

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// DetectSize is the side of the square YuNet reads, and FaceSize that of the
// aligned face SFace does.
const (
	DetectSize = 640
	FaceSize   = 112
)

// FaceModels are YuNet and SFace as the bundle in one directory holds them:
// yunet.ort, sface.ort, and NAME, which is what a stored face records it was
// read by. What the tensors mean is the caller's: these only run them.
type FaceModels struct {
	Name string

	detector, recognizer *tower
}

// OpenFaces checks the bundle is all there and starts nothing.
func OpenFaces(dir, helper string) (*FaceModels, error) {
	name, err := os.ReadFile(filepath.Join(dir, "NAME")) //nolint:gosec // the model directory this server was given
	if err != nil {
		return nil, fmt.Errorf("embed: the face models in %s: %w", dir, err)
	}
	for _, f := range []string{"yunet.ort", "sface.ort"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, fmt.Errorf("embed: the face models in %s: %w", dir, err)
		}
	}
	return &FaceModels{
		Name:       strings.TrimSpace(string(name)),
		detector:   &tower{bin: helper, model: filepath.Join(dir, "yunet.ort"), idle: Idle},
		recognizer: &tower{bin: helper, model: filepath.Join(dir, "sface.ort"), idle: Idle},
	}, nil
}

// Detect runs YuNet over one picture, 3 x DetectSize x DetectSize planes of
// BGR from 0 to 255, and answers its twelve outputs in the graph's order:
// cls, obj, bbox and kps, each at strides 8, 16 and 32.
func (m *FaceModels) Detect(ctx context.Context, bgr []float32) ([][]float32, error) {
	return m.detector.run(ctx, dtypeFloat32, []uint32{1, 3, DetectSize, DetectSize}, float32s(bgr))
}

// Recognize runs SFace over one aligned face, 3 x FaceSize x FaceSize planes of
// RGB from 0 to 255, and answers its 128 numbers, not normalised.
func (m *FaceModels) Recognize(ctx context.Context, rgb []float32) ([]float32, error) {
	return one(m.recognizer.run(ctx, dtypeFloat32, []uint32{1, 3, FaceSize, FaceSize}, float32s(rgb)))
}

// Close stops both processes.
func (m *FaceModels) Close() {
	m.detector.stop()
	m.recognizer.stop()
}

func float32s(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}
