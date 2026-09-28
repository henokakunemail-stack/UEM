package software

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrQueueFull is returned when an agent's work queue is already saturated.
var ErrQueueFull = errors.New("software work queue is full")

// queueDepth is how many tasks may wait behind the one that is running.
//
// One slot is enough and is the important number: this agent serves exactly one
// device, so a task queued here is a task that was dispatched by the server and
// has not started. The old code spawned a goroutine per command with no limit
// at all, so a deployment aimed at this device, a retry, and an operator's
// manual trigger could all be running installers at once, against the same
// Program Files, on a machine somebody is using.
//
// installs and uninstalls share this one queue on purpose. Two installers in
// parallel collide through MSI's own service and produce failures that look
// like corrupt packages. An uninstall racing an install of the same product is
// the same collision, and it is the one an operator triggers by clicking
// retry on a slow install. Serialising them costs a little throughput and buys
// the property that a machine's package state is only ever mutated by one
// process at a time.
const queueDepth = 2

// TaskQueue runs software tasks one at a time per agent.
//
// A single worker goroutine draining a bounded channel, rather than a mutex
// per operation, because a mutex can only reject the second caller while a
// queue also absorbs the burst: a deployment to this device, then a manual
// trigger, then a retry all land, the first runs and the rest wait their turn
// instead of two of the three failing outright. The maintenance engine in this
// codebase already uses a TryLock and deliberately rejects for the same
// underlying reason -- two concurrent privileged sweeps on one box is a class
// of bug -- but an install takes minutes, so rejecting a queued task during one
// would fail a deployment the server believes it dispatched.
type TaskQueue struct {
	tasks chan task

	// inflight counts the task currently running plus everything waiting. It is
	// separate from len(tasks) because a task is removed from the channel
	// before it runs, and a queue whose occupancy drops the moment work starts
	// can accept unbounded work.
	mu       sync.Mutex
	inflight int

	// stopped is closed by Close. A worker that is mid-task finishes it; the
	// channel is not drained or cancelled, because abandoning an installer
	// halfway is exactly the half-installed state this design exists to avoid.
	stopped chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// task is one unit of queued work, carrying the identifiers that make the
// agent log readable when a fleet of devices is all doing this at once.
type task struct {
	id   string
	kind string
	run  func()
}

// NewTaskQueue starts the single worker and returns it ready to accept work.
func NewTaskQueue() *TaskQueue {
	q := &TaskQueue{
		tasks:   make(chan task, queueDepth),
		stopped: make(chan struct{}),
	}
	q.wg.Add(1)
	go q.work()
	return q
}

// Submit enqueues fn and reports whether it was accepted.
//
// Rejection is deliberate and bounded, not a backpressure valve: past the
// limit the caller is told no, and the caller is the command dispatcher, which
// turns that into a failed task with a message an operator can act on. Silently
// dropping instead would leave a task the server dispatched and will wait
// forever for.
func (q *TaskQueue) Submit(taskID, kind string, fn func()) error {
	q.mu.Lock()
	if q.inflight >= queueDepth+1 {
		behind := q.inflight - 1
		q.mu.Unlock()
		return fmt.Errorf("%w: %s %s is already queued behind %d other task(s)",
			ErrQueueFull, kind, shortID(taskID), behind)
	}
	q.inflight++
	position := q.inflight
	q.mu.Unlock()

	wrapped := func() {
		defer q.done()
		fn()
	}
	select {
	case q.tasks <- task{id: taskID, kind: kind, run: wrapped}:
		log.Info().Str("task", shortID(taskID)).Str("kind", kind).Int("position", position).Msg("software task queued")
		return nil
	case <-q.stopped:
		q.done()
		return errors.New("agent is shutting down")
	}
}

func (q *TaskQueue) done() {
	q.mu.Lock()
	q.inflight--
	q.mu.Unlock()
}

func (q *TaskQueue) work() {
	defer q.wg.Done()
	for t := range q.tasks {
		func() {
			// A panic in one task must not take the worker -- and with it every
			// later task on this device -- down. The install path already
			// recovers in main.go, but the queue outlives that call site and a
			// future caller will not remember.
			defer func() {
				if r := recover(); r != nil {
					log.Error().
						Str("task", shortID(t.id)).
						Str("kind", t.kind).
						Interface("panic", r).
						Msg("recovered panic in software task")
				}
			}()
			log.Info().Str("task", shortID(t.id)).Str("kind", t.kind).Msg("software task starting")
			t.run()
		}()
	}
}

// Close stops accepting work and waits for the running task to finish.
func (q *TaskQueue) Close() {
	q.once.Do(func() {
		close(q.stopped)
		close(q.tasks)
		q.wg.Wait()
	})
}

// shortID keeps log lines readable. Task ids are UUIDs and the full value adds
// a column of hex to every line without making the log easier to match up.
func shortID(id string) string {
	const keep = 8
	if len(id) <= keep {
		return id
	}
	return id[:keep]
}

// installTimeout is how long one package process may run.
//
// The ceiling is generous because it is only a backstop: the real bound on a
// good install is the installer exiting. What this catches is the case the
// console cannot otherwise diagnose -- a package whose switches are wrong for
// it in a way that opens a modal prompt the endpoint's user never answers, or a
// package that hangs on a network share. Those tasks otherwise sit in
// 'installing' until an operator goes looking.
//
// It is deliberately much larger than any real install. A 4 GiB enterprise MSI
// on a slow disk is still well inside 20 minutes, and a timeout that fires
// during a legitimate large install is worse than useless: it kills a working
// installer mid-write and leaves the machine half-configured.
const installTimeout = 20 * time.Minute

// taskContext is the context one install or uninstall runs under: a deadline on
// the process, independent of the download that precedes it.
//
// The download has its own 30-minute HTTP client and the two limits are not
// the same kind of thing -- a slow mirror should not eat the installer's
// budget, and a hung installer should not be blamed on the network.
func taskContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, installTimeout)
}
