package softwaredeployment

import (
	"context"
	"database/sql"
	"time"
)

// SweepInterval is how often AbandonOrphanedTasks runs. It matches the device
// offline sweep's 15s cadence, so a task is reaped within one tick of the agent
// that owned it being declared offline, and the two events stay in a
// predictable order: the device goes offline first, then its task.
const SweepInterval = 15 * time.Second

// AbandonGrace is how long a task may sit in a running state before the sweep
// assumes the agent that owned it is gone.
//
// It is not a timeout on the install itself -- the agent enforces its own
// 20-minute process deadline and reports a real failure when it trips. This is
// the backstop for the case where no report can be sent at all, so the only
// question it answers is "could this agent still be working on it?". The
// value is generous because the sweep is only reached when the device is
// already offline, and an agent that reconnects after a short network blip
// will still be running its installer.
const AbandonGrace = 5 * time.Minute

// AbandonOrphanedTasks moves tasks stuck in a running state on an offline
// device to 'failed_lost', and rolls up any deployment that was waiting on them.
//
// It is a single set-based UPDATE plus the rollups, not a read-modify-write per
// task: this runs every 15 seconds on a table that can hold a row per device
// per rollout, and a fleet-sized deployment would otherwise mean thousands of
// write transactions per sweep, which is the SQLite write-lock contention the
// rest of this server is built to avoid.
//
// A task is only reaped when BOTH conditions hold: the device is not currently
// connected, and the task has not been updated within AbandonGrace. A live
// device is never touched, so a long install on a healthy machine cannot be
// reaped out from under the agent that is still running it -- a task in
// 'installing' is not evidence of anything until the machine is gone.
//
// ponytail: liveness is taken from the device row, which the offline sweeper
// updates on the same cadence, rather than from the hub directly. That means a
// reaped task is at most one tick older than the truth. Querying the hub here
// instead would be tighter but would make this module depend on the transport
// package; the extra 15s costs nothing, because either way the task was already
// unreachable when the device dropped.
func (r *Repository) AbandonOrphanedTasks(ctx context.Context, grace time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-grace)
	now := time.Now().UTC()

	const msg = "agent stopped reporting before this task finished; the endpoint went offline mid-deployment. The package may be partially installed - check inventory and re-deploy if needed."

	res, err := r.db.ExecContext(ctx, `
		UPDATE deployment_tasks
		SET status = ?, error_message = ?, updated_at = ?, completed_at = ?
		WHERE status IN ('dispatched', 'downloading', 'installing')
		  AND updated_at < ?
		  AND device_id IN (SELECT id FROM devices WHERE status != 'online')
		  AND completed_at IS NULL`, TaskStatusFailedLost, msg, now, now, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return 0, err
	}

	// The rollup lives in syncDeploymentStatus, which needs a task id, so the
	// parents are collected here. Distinct because a device rolled out to many
	// deployments has one row per parent and the list is short even when the
	// fleet is large.
	var deploymentIDs []string
	if err := r.db.SelectContext(ctx, &deploymentIDs, `
		SELECT DISTINCT deployment_id FROM deployment_tasks
		WHERE status = ? AND updated_at >= ?`, TaskStatusFailedLost, now); err != nil && err != sql.ErrNoRows {
		return n, err
	}
	for _, id := range deploymentIDs {
		if err := r.syncDeploymentStatusByID(ctx, id); err != nil {
			return n, err
		}
	}
	return n, nil
}
