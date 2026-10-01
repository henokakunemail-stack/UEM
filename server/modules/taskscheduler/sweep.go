package taskscheduler

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
)

// SweepInterval is how often StartSweep reaps orphaned device runs. It matches
// the software-deployment sweep so a task is reaped within one tick of its
// agent being declared offline, and the two events keep a predictable order.
const SweepInterval = 15 * time.Second

// AbandonGrace is how long a device run may sit in a non-terminal state before
// the sweep assumes the agent that owned it is gone.
//
// It is not a timeout on the script. The executor enforces its own deadline and
// reports a real failure when it trips; this is the backstop for the case where
// no report can be sent at all, so the only question it answers is "could this
// agent still be working on it?". The value matches software-deployment's.
const AbandonGrace = 5 * time.Minute

// AbandonOrphanedRuns moves device runs stuck in a non-terminal state whose
// device is gone to 'failed', and rolls up every parent that was waiting on
// them. It returns how many device runs were reaped.
//
// Without this, the parent has no path to ever close. SyncRunStatus only runs
// from the agent result handler, and returns early unless every device run is
// terminal -- so an endpoint that took the command and then vanished left its
// row at 'dispatched' and its parent at 'running' with a NULL completed_at for
// the life of the installation. The operator saw a run that had already
// dispatched, was already offline, and could never finish.
//
// A run is reaped only when BOTH conditions hold: the device is not currently
// connected, and the run has been non-terminal for longer than AbandonGrace. A
// live device is never touched, so a long script on a healthy machine cannot be
// reaped out from under the agent still running it.
//
// ponytail: liveness comes from the device row, which the offline sweeper
// updates on the same cadence, rather than from the hub. A reaped run is then at
// most one tick older than the truth; asking the hub would make this module
// depend on the transport package, and the extra 15s costs nothing because the
// run was already unreachable when the device dropped.
func (r *Repository) AbandonOrphanedRuns(ctx context.Context, grace time.Duration) (int64, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-grace)

	// The parents are collected before the UPDATE. Picking them afterwards would
	// mean identifying the rows just written by their timestamp -- a string
	// comparison on a column the driver formats per value -- and it would also
	// re-pick runs reaped by an earlier sweep, rolling them up over and over.
	// Here it is exactly the set of runs about to need a rollup, and the rollup
	// itself is idempotent.
	var runIDs []string
	if err := r.db.SelectContext(ctx, &runIDs, `
		SELECT DISTINCT dr.run_id
		FROM scheduled_task_device_runs dr
		JOIN devices d ON d.id = dr.device_id
		WHERE dr.status IN ('pending', 'dispatched')
		  AND dr.completed_at IS NULL
		  AND dr.started_at < ?
		  AND d.status != 'online'`, cutoff); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if len(runIDs) == 0 {
		return 0, nil
	}

	// One set-based UPDATE rather than a read-modify-write per row: this runs
	// every 15 seconds and the table holds a row per device per run.
	const msg = "agent stopped reporting before this script finished; the endpoint went offline mid-run. The script may still be executing on the device."

	res, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_task_device_runs
		SET status = 'failed', error_message = ?, completed_at = ?
		WHERE status IN ('pending', 'dispatched')
		  AND completed_at IS NULL
		  AND started_at < ?
		  AND device_id IN (SELECT id FROM devices WHERE status != 'online')`,
		msg, now, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}

	for _, runID := range runIDs {
		if err := r.rollupRunByID(ctx, runID); err != nil {
			return n, err
		}
	}
	return n, nil
}

// StartSweep reaps orphaned device runs until ctx is cancelled.
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
				if _, err := r.AbandonOrphanedRuns(ctx, grace); err != nil {
					// Logged, not fatal. A sweep that stops on the first
					// transient error is a sweep that has silently stopped;
					// the next tick is fifteen seconds away.
					log.Warn().Err(err).Msg("scheduled task sweep: reap orphaned device runs")
				}
			}
		}
	}()
}
