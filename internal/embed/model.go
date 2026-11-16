package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Idle is how long a tower waits for another call before its process goes.
// Ten minutes covers a search session and a batch of the pass, and a process
// that is gone costs its next caller about a second.
const Idle = 10 * time.Minute

// Model is SigLIP2 as the bundle in one directory holds it: vision.ort,
// text.ort, tokenizer.bin, and NAME, which is what a stored vector records it
// was made by -- two bundles that disagree on it are two vector spaces.
type Model struct {
	Name string

	vision, text *tower
	tokenizer    string

	// The tokenizer is tens of megabytes of maps, held while the text tower
	// is and dropped with it.
	tokMu sync.Mutex
	tok   *Tokenizer
}

// Open checks the bundle is all there and starts nothing.
func Open(dir, helper string) (*Model, error) {
	name, err := os.ReadFile(filepath.Join(dir, "NAME")) //nolint:gosec // the model directory this server was given
	if err != nil {
		return nil, fmt.Errorf("embed: the model in %s: %w", dir, err)
	}
	for _, f := range []string{"vision.ort", "text.ort", "tokenizer.bin"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, fmt.Errorf("embed: the model in %s: %w", dir, err)
		}
	}
	m := &Model{
		Name:      strings.TrimSpace(string(name)),
		vision:    &tower{bin: helper, model: filepath.Join(dir, "vision.ort"), idle: Idle},
		text:      &tower{bin: helper, model: filepath.Join(dir, "text.ort"), idle: Idle},
		tokenizer: filepath.Join(dir, "tokenizer.bin"),
	}
	m.text.stopped = func() {
		m.tokMu.Lock()
		m.tok = nil
		m.tokMu.Unlock()
	}
	return m, nil
}

// Image is the vector of a picture: unit length, so a dot product with a
// query's is their similarity.
func (m *Model) Image(ctx context.Context, img image.Image) ([]float32, error) {
	return one(m.vision.run(ctx, dtypeFloat32, []uint32{1, 3, ImageSize, ImageSize}, float32s(Pixels(img))))
}

// Text is the vector of a query, in the same space as Image's.
func (m *Model) Text(ctx context.Context, query string) ([]float32, error) {
	tok, err := m.loadTokenizer()
	if err != nil {
		return nil, err
	}
	ids := tok.Encode(query)
	data := make([]byte, 8*len(ids))
	for i, id := range ids {
		binary.LittleEndian.PutUint64(data[8*i:], uint64(id)) //nolint:gosec // a token id, never negative
	}
	return one(m.text.run(ctx, dtypeInt64, []uint32{1, ContextLength}, data))
}

// loadTokenizer never holds tokMu while a tower's lock is taken: the tower
// takes tokMu when it stops, so the other order would be a deadlock.
func (m *Model) loadTokenizer() (*Tokenizer, error) {
	m.tokMu.Lock()
	defer m.tokMu.Unlock()
	if m.tok != nil {
		return m.tok, nil
	}
	raw, err := os.ReadFile(m.tokenizer)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	tok, err := ReadTokenizer(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	m.tok = tok
	return tok, nil
}

// Close stops both processes.
func (m *Model) Close() {
	m.vision.stop()
	m.text.stop()
}
