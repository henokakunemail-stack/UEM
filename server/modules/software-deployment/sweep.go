package softwaredeployment

import (
	"context"
	"database/sql"
	"errors"
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

// NoProgressTimeout is how long a sent task may go without any report on a
// device that is still online. The agent enforces a 20-minute install
// deadline and reports it, so an hour of silence means the command was lost
// or the agent died without dropping its socket.
const NoProgressTimeout = 60 * time.Minute

// QueuedTaskExpiry is how long a task waits for its device to come online.
const QueuedTaskExpiry = 7 * 24 * time.Hour

// AbandonOrphanedTasks moves tasks stuck in a running state on an offline
// device to 'failed_lost', and rolls up any deployment that was waiting on them.
//
// It is a single set-based UPDATE plus the rollups, not a read-modify-write per
// task: this runs every 15 seconds on a table that can hold a row per device
// per rollout, and a fleet-sized deployment would otherwise mean thousands of
// write transactions per sweep, which is the SQLite write-lock contention the
// rest of this server is built to avoid.
//
// Three things are reaped:
//   - a sent task on a device that is not connected and has not reported
//     within AbandonGrace;
//   - a sent task with no report for NoProgressTimeout even though the device
//     is online -- the command was lost, or the agent died holding its socket;
//   - a task still pending after QueuedTaskExpiry: the device never came back.
//
// An online device's task gets an hour, not five minutes: a long install on a
// healthy machine must not be reaped out from under the agent running it.
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
	const stuckMsg = "no_progress: the agent sent no report for an hour while online; the command may not have reached it. The package may be partially installed - check inventory and re-deploy if needed."
	const neverMsg = "never_connected: the device did not come online within 7 days, so the task was never sent."

	var n int64
	for _, rule := range []struct {
		where string
		msg   string
		args  []any
	}{
		{`status IN ('dispatched', 'downloading', 'installing', 'uninstalling') AND updated_at < ?
		  AND device_id IN (SELECT id FROM devices WHERE status != 'online')`, msg, []any{cutoff}},
		{`status IN ('dispatched', 'downloading', 'installing', 'uninstalling') AND updated_at < ?`,
			stuckMsg, []any{now.Add(-NoProgressTimeout)}},
		{`status = 'pending' AND created_at < ?`, neverMsg, []any{now.Add(-QueuedTaskExpiry)}},
	} {
		args := append([]any{TaskStatusFailedLost, rule.msg, now, now}, rule.args...)
		res, err := r.db.ExecContext(ctx, `
		UPDATE deployment_tasks
		SET status = ?, error_message = ?, updated_at = ?, completed_at = ?
		WHERE `+rule.where+` AND completed_at IS NULL`, args...)
		if err != nil {
			return n, err
		}
		reaped, err := res.RowsAffected()
		if err != nil {
			return n, err
		}
		n += reaped
	}
	if n == 0 {
		return 0, nil
	}

	// The rollup lives in syncDeploymentStatus, which needs a task id, so the
	// parents are collected here. Distinct because a device rolled out to many
	// deployments has one row per parent and the list is short even when the
	// fleet is large.
	var deploymentIDs []string
	if err := r.db.SelectContext(ctx, &deploymentIDs, `
		SELECT DISTINCT deployment_id FROM deployment_tasks
		WHERE status = ? AND updated_at >= ?`, TaskStatusFailedLost, now); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return n, err
	}
	for _, id := range deploymentIDs {
		if err := r.syncDeploymentStatusByID(ctx, id); err != nil {
			return n, err
		}
	}
	return n, nil
}
