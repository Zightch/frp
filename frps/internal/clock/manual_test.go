package clock

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestManualEveryRunsOnAdvance(t *testing.T) {
	start := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	manual := NewManual(start)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fired := make([]time.Time, 0, 2)
	task := manual.Every(ctx, "runtime.scan", 5*time.Second, func(_ context.Context, firedAt time.Time) {
		fired = append(fired, firedAt)
	})

	if task == nil {
		t.Fatal("expected manual scheduler to return a task")
	}

	manual.Advance(4 * time.Second)
	if len(fired) != 0 {
		t.Fatalf("job should not run before due time: %#v", fired)
	}

	manual.Advance(time.Second)
	if got, want := fired, []time.Time{start.Add(5 * time.Second)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected fired schedule: got %#v want %#v", got, want)
	}

	manual.Advance(20 * time.Second)
	if got, want := fired, []time.Time{start.Add(5 * time.Second), start.Add(10 * time.Second)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected fired schedule after large advance: got %#v want %#v", got, want)
	}
}
