package inventory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type mockCollector struct{}

func (m *mockCollector) Collect() (Report, error) {
	return Report{Hardware: Hardware{RAMTotalBytes: 1024}}, nil
}

type mockReporter struct {
	count atomic.Int32
}

func (m *mockReporter) ReportInventory(ctx context.Context, rep Report) error {
	m.count.Add(1)
	return nil
}

func TestInitialOffset(t *testing.T) {
	window := 30 * time.Minute
	if off := initialOffset("", window); off != 0 {
		t.Fatalf("expected 0 for empty deviceID, got %v", off)
	}
	if off := initialOffset("dev-1", 0); off != 0 {
		t.Fatalf("expected 0 for zero window, got %v", off)
	}

	off1 := initialOffset("device-abc-123", window)
	off2 := initialOffset("device-abc-123", window)
	if off1 != off2 {
		t.Fatalf("expected deterministic hash offset, got %v and %v", off1, off2)
	}
	if off1 < 0 || off1 >= window {
		t.Fatalf("offset %v out of bounds [0, %v)", off1, window)
	}
}

func TestScheduler_ImmediateAndCancel(t *testing.T) {
	col := &mockCollector{}
	rep := &mockReporter{}
	s := NewScheduler(col, rep, "dev-test", 10*time.Second, 1*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		s.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("scheduler did not stop after context cancel")
	}

	if rep.count.Load() != 1 {
		t.Fatalf("expected exactly 1 immediate collection, got %d", rep.count.Load())
	}
}
