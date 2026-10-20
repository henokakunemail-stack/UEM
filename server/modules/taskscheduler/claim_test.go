package taskscheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A due schedule is fired by exactly one claimer. Two pollers -- two server
// nodes, or a poll overlapping a slow previous one -- both read it as due; if
// both fired, the script ran twice on every target.
func TestOnlyOneClaimerFiresADueSchedule(t *testing.T) {
	_, sched, id := pollFixture(t)
	now := time.Now().UTC()

	var wins atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := sched.repo.ClaimDueRun(context.Background(), id, now, time.Hour)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claimers fired the same due schedule, want exactly 1", wins.Load())
	}

	// And once claimed it is no longer due until the interval passes again.
	if ok, err := sched.repo.ClaimDueRun(context.Background(), id, now.Add(time.Minute), time.Hour); err != nil || ok {
		t.Fatalf("a just-claimed schedule was claimed again a minute later (ok=%v err=%v)", ok, err)
	}
	if ok, err := sched.repo.ClaimDueRun(context.Background(), id, now.Add(61*time.Minute), time.Hour); err != nil || !ok {
		t.Fatalf("the schedule was not claimable after its interval (ok=%v err=%v)", ok, err)
	}
}

// A disabled schedule is never claimed, even when overdue.
func TestADisabledScheduleIsNotClaimed(t *testing.T) {
	database, sched, id := pollFixture(t)
	if _, err := database.Exec(`UPDATE task_schedules SET is_enabled = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := sched.repo.ClaimDueRun(context.Background(), id, time.Now().UTC(), time.Hour); err != nil || ok {
		t.Fatalf("disabled schedule claimed (ok=%v err=%v)", ok, err)
	}
}
