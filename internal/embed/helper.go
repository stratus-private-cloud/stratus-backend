package embed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable is a tower that could not answer -- not started, killed,
// talking nonsense -- as opposed to one that answered with an error about
// the input. It says nothing about the picture or the query, so a pass that
// meets it tries again later rather than writing a verdict.
var ErrUnavailable = errors.New("embed: the helper is unavailable")

// LookupHelper finds stratus-embed. Unlike ffmpeg it is not required: without
// it, searching by content is the one thing missing, and the setting that
// turns it on says so.
func LookupHelper() string {
	path, err := exec.LookPath("stratus-embed")
	if err != nil {
		return ""
	}
	return path
}

// tower is one of the model's halves in a process of its own, started by the
// first call and stopped after idle without one. One call at a time: the
// process runs one thread, and a second call could only queue inside it.
type tower struct {
	bin, model string
	idle       time.Duration
	stopped    func() // called with mu held when the process goes

	mu    sync.Mutex
	proc  *process
	timer *time.Timer
}

type process struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *tailBuffer
}

const (
	dtypeFloat32 = 1
	dtypeInt64   = 7
)

// run sends one tensor and reads the vector back.
func (t *tower) run(ctx context.Context, dtype byte, dims []uint32, data []byte) ([]float32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.proc == nil {
		p, err := start(t.bin, t.model) //nolint:contextcheck // outlives the call that starts it
		if err != nil {
			return nil, err
		}
		t.proc = p
	}
	if t.timer != nil {
		t.timer.Stop()
	}

	// A call abandoned halfway leaves a frame half read, and there is nothing
	// in the protocol to find the next one by, so the process goes with it.
	p := t.proc
	unwatch := context.AfterFunc(ctx, func() { _ = p.cmd.Process.Kill() })
	v, err := p.call(dtype, dims, data)
	unwatch()

	var answered *answerError
	switch {
	case err == nil, errors.As(err, &answered):
		t.timer = time.AfterFunc(t.idle, t.stopIdle)
		return v, err
	case ctx.Err() != nil:
		t.stopLocked()
		return nil, ctx.Err()
	default:
		msg := strings.TrimSpace(p.stderr.String())
		t.stopLocked()
		return nil, fmt.Errorf("%w: %s: %w %s", ErrUnavailable, t.model, err, msg)
	}
}

func (t *tower) stopIdle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

// stop ends the process now, which is also how the server shuts it down.
func (t *tower) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
}

func (t *tower) stopLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	if t.proc == nil {
		return
	}
	// EOF is the helper's way out; Kill is for one that is not reading.
	_ = t.proc.in.Close()
	done := make(chan struct{})
	go func(p *process) {
		_ = p.cmd.Wait()
		close(done)
	}(t.proc)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = t.proc.cmd.Process.Kill()
		<-done
	}
	t.proc = nil
	if t.stopped != nil {
		t.stopped()
	}
}

func start(bin, model string) (*process, error) {
	if bin == "" {
		return nil, fmt.Errorf("%w: stratus-embed is not installed", ErrUnavailable)
	}
	// Not a request's context: the process lives as long as its tower decides,
	// and stop is what ends it.
	cmd := exec.CommandContext(context.Background(), bin, model) //nolint:gosec // the helper found on PATH and the model's own file
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return &process{cmd: cmd, in: in, out: bufio.NewReader(out), stderr: stderr}, nil
}

// answerError is the helper refusing an input, with the process still sound.
type answerError struct{ msg string }

func (e *answerError) Error() string { return "embed: " + e.msg }

// maxVector bounds what a reply may claim to carry, so a corrupt header is an
// error and not an allocation.
const maxVector = 1 << 16

func (p *process) call(dtype byte, dims []uint32, data []byte) ([]float32, error) {
	head := make([]byte, 2, 2+4*len(dims))
	head[0], head[1] = dtype, byte(len(dims)) //nolint:gosec // a rank of two
	for _, d := range dims {
		head = binary.LittleEndian.AppendUint32(head, d)
	}
	if _, err := p.in.Write(append(head, data...)); err != nil {
		return nil, err
	}

	var reply [8]byte
	if _, err := io.ReadFull(p.out, reply[:]); err != nil {
		return nil, err
	}
	status, n := binary.LittleEndian.Uint32(reply[:4]), binary.LittleEndian.Uint32(reply[4:])
	if n > maxVector*4 {
		return nil, fmt.Errorf("a reply of %d", n)
	}
	if status != 0 {
		msg := make([]byte, n)
		if _, err := io.ReadFull(p.out, msg); err != nil {
			return nil, err
		}
		return nil, &answerError{string(msg)}
	}
	raw := make([]byte, 4*n)
	if _, err := io.ReadFull(p.out, raw); err != nil {
		return nil, err
	}
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return v, nil
}

// tailBuffer keeps the last of what the helper said on stderr, for the error
// that explains why it went.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const stderrKept = 4 << 10

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if over := b.buf.Len() - stderrKept; over > 0 {
		b.buf.Next(over)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
