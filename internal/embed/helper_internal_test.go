package embed

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"testing"
	"time"
)

// The test binary is its own fake helper: run with STRATUS_FAKE_EMBED set, it
// speaks the protocol instead of testing. "echo" answers two outputs, the sum
// of the input as a one-element vector and the input itself, and a negative
// sum as a refusal; "die" exits on the first request.
func TestMain(m *testing.M) {
	switch os.Getenv("STRATUS_FAKE_EMBED") {
	case "":
		os.Exit(m.Run())
	case "die":
		_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
		os.Exit(9)
	default:
		fakeHelper()
	}
}

func fakeHelper() {
	for {
		head := make([]byte, 2)
		if _, err := io.ReadFull(os.Stdin, head); err != nil {
			os.Exit(0)
		}
		dims := make([]uint32, head[1])
		_ = binary.Read(os.Stdin, binary.LittleEndian, dims)
		n := 1
		for _, d := range dims {
			n *= int(d)
		}
		vals := make([]float32, n)
		_ = binary.Read(os.Stdin, binary.LittleEndian, vals)
		var sum float32
		for _, v := range vals {
			sum += v
		}
		if sum < 0 {
			msg := "negative"
			_ = binary.Write(os.Stdout, binary.LittleEndian, []uint32{1, uint32(len(msg))})
			_, _ = os.Stdout.WriteString(msg)
			continue
		}
		reply := []uint32{0, 2, 1, math.Float32bits(sum), uint32(n)} //nolint:gosec // a test's few values
		for _, v := range vals {
			reply = append(reply, math.Float32bits(v))
		}
		_ = binary.Write(os.Stdout, binary.LittleEndian, reply)
	}
}

func fakeTower(t *testing.T, mode string, idle time.Duration) *tower {
	t.Helper()
	t.Setenv("STRATUS_FAKE_EMBED", mode)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tw := &tower{bin: self, model: "fake", idle: idle}
	t.Cleanup(tw.stop)
	return tw
}

func floats(v ...float32) []byte {
	b := make([]byte, 0, 4*len(v))
	for _, f := range v {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
	}
	return b
}

func TestATowerAnswersAndStaysUp(t *testing.T) {
	tw := fakeTower(t, "echo", time.Hour)
	ctx := context.Background()
	for i := range 3 {
		v, err := tw.run(ctx, dtypeFloat32, []uint32{1, 2}, floats(1, float32(i)))
		if err != nil || len(v) != 2 || len(v[0]) != 1 || v[0][0] != float32(1+i) ||
			len(v[1]) != 2 || v[1][0] != 1 || v[1][1] != float32(i) {
			t.Fatalf("call %d: %v %v", i, v, err)
		}
	}
	tw.mu.Lock()
	pid := tw.proc.cmd.Process.Pid
	tw.mu.Unlock()

	// A refusal is about the input: the process is the same one afterwards.
	var answered *answerError
	if _, err := tw.run(ctx, dtypeFloat32, []uint32{1}, floats(-1)); !errors.As(err, &answered) {
		t.Fatalf("a refusal came back as %v", err)
	}
	if _, err := tw.run(ctx, dtypeFloat32, []uint32{1}, floats(2)); err != nil {
		t.Fatal(err)
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.proc.cmd.Process.Pid != pid {
		t.Error("a refusal restarted the process")
	}
}

func TestATowerStopsWhenIdleAndStartsAgain(t *testing.T) {
	tw := fakeTower(t, "echo", 50*time.Millisecond)
	stops := 0
	tw.stopped = func() { stops++ }
	ctx := context.Background()
	if _, err := tw.run(ctx, dtypeFloat32, []uint32{1}, floats(1)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tw.mu.Lock()
		gone := tw.proc == nil
		tw.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the process outlived its idle time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v, err := tw.run(ctx, dtypeFloat32, []uint32{1}, floats(4)); err != nil || v[0][0] != 4 {
		t.Fatalf("after idling: %v %v", v, err)
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if stops != 1 {
		t.Errorf("stopped %d times, want 1", stops)
	}
}

func TestATowerThatDiesIsUnavailable(t *testing.T) {
	tw := fakeTower(t, "die", time.Hour)
	_, err := tw.run(context.Background(), dtypeFloat32, []uint32{1}, floats(1))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.proc != nil {
		t.Error("a dead process was kept")
	}
}

func TestNoHelperIsUnavailable(t *testing.T) {
	tw := &tower{model: "fake", idle: time.Hour}
	if _, err := tw.run(context.Background(), dtypeFloat32, []uint32{1}, floats(1)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
}

func TestAModelOfOneOutputRefusesAGraphOfMore(t *testing.T) {
	tw := fakeTower(t, "echo", time.Hour)
	if _, err := one(tw.run(context.Background(), dtypeFloat32, []uint32{1}, floats(1))); !errors.Is(err, ErrUnavailable) {
		t.Errorf("two outputs read as one: %v", err)
	}
}
