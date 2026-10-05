package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
)

// SweepInterval is how often StartSweep reaps orphaned tasks. It matches the
// software-deployment and scheduled-task sweeps, so every stranded row in this
// server is reaped on the same cadence.
const SweepInterval = 15 * time.Second

// AbandonGrace is how long a task may sit without a step report before the
// sweep assumes the agent that owned it will never send one.
//
// It is not a timeout on the work. A step that is still running posts a
// heartbeat every 10 minutes, so the longest legitimate gap between two writes
// to a live row is one heartbeat, and Grace is twice that plus room for a slow
// round trip.
//
// Grace used to have to exceed the longest step ceiling (stepTimeout on the
// agent), and it did. It no longer has to, and the relationship is now the
// other way round: disk_check's ceiling is 30 minutes against a 20-minute
// grace. That is safe only because of the heartbeat — a step silent for its
// whole duration would be reaped mid-scan, which is what happened before the
// heartbeat existed and is why a ceiling cannot be raised without one.
//
// If you raise a step ceiling past AbandonGrace anywhere, the heartbeat is
// already required, and this is the number to re-check against it. If you
// shorten this, the agent's heartbeatInterval has to come down with it.
//
// ponytail: unlike the two sibling sweeps, this one has no "device is offline"
// predicate. That predicate alone would have missed the case that made this
// sweeper necessary — a task stranded on a machine that stayed connected the
// whole time, because the agent's rejection was posted against a step name the
// server refused. Age is the whole condition here, and it is the condition that
// actually describes "no report is coming".
const AbandonGrace = 20 * time.Minute

// AbandonOrphanedTasks moves tasks stranded in a non-terminal state to 'failed'
// and rolls up every job that was waiting on them. It returns how many tasks
// were reaped.
//
// Without this there is no path to a terminal state at all: the job's status is
// written only by recomputeJobCounters, which only runs when a task reaches a
// terminal state, so one silent task holds its job at 'running' for the life of
// the installation. The console then polls that job every three seconds
// forever, and its "Running Now" count is permanently inflated.
//
// The parents are collected before the UPDATE so the set is exactly the jobs
// about to need a rollup — picking them out afterwards would re-pick rows an
// earlier sweep already reaped, rolling them up again on every tick. The
// rollup itself is idempotent (it counts rather than increments), so a
// duplicate is harmless; it is wasted writes that are avoided.
//
// ponytail: a set-based UPDATE, not a read-modify-write per task. This runs
// every 15 seconds on a table with a row per device per job.
func (r *Repository) AbandonOrphanedTasks(ctx context.Context, grace time.Duration) (int64, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-grace)

	var jobIDs []string
	if err := r.db.SelectContext(ctx, &jobIDs, `
		SELECT DISTINCT job_id FROM maintenance_tasks
		WHERE status IN ('pending', 'dispatched', 'running')
		  AND updated_at < ?`, cutoff); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if len(jobIDs) == 0 {
		return 0, nil
	}

	const msg = "agent stopped reporting before this maintenance task finished; " +
		"the endpoint may have gone offline or restarted mid-task. Re-run it if the work is still needed."

	res, err := r.db.ExecContext(ctx, `
		UPDATE maintenance_tasks
		SET status = ?, error_message = COALESCE(error_message, ?),
		    completed_at = COALESCE(completed_at, ?), updated_at = ?
		WHERE status IN ('pending', 'dispatched', 'running')
		  AND updated_at < ?`,
		TaskStatusFailed, msg, now, now, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}

	for _, id := range jobIDs {
		if err := r.recomputeJob(ctx, id); err != nil {
			return n, err
		}
	}
	return n, nil
}

// recomputeJob runs the existing recount for one job outside a caller-owned
// transaction. recomputeJobCounters takes a *sqlx.Tx because every other caller
// is already inside one; this sweep writes a batch first and then needs the
// same recount per parent, so it owns the transaction itself.
func (r *Repository) recomputeJob(ctx context.Context, jobID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := recomputeJobCounters(ctx, tx, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// StartSweep reaps orphaned tasks until ctx is cancelled.
//
// The context is not decoration: without it this loop outlives the process it
// belongs to, and there is nowhere else to stop it.
func (r *Repository) StartSweep(ctx context.Context, interval, grace time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		// Inside the goroutine: this function returns as soon as the goroutine
		// is launched, so a defer here would stop the ticker before it ever
		// fired and the sweep would silently never run.
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := r.AbandonOrphanedTasks(ctx, grace); err != nil {
					// Logged, not fatal. A sweep that stops on the first
					// transient error is a sweep that has silently stopped;
					// the next tick is fifteen seconds away.
					log.Warn().Err(err).Msg("maintenance sweep: reap orphaned tasks")
				}
			}
		}
	}()
}
