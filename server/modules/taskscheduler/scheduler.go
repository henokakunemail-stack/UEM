package taskscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
)

type Hub interface {
	Online(deviceID string) bool
	SendTo(deviceID string, msg []byte) bool
}

type Scheduler struct {
	repo *Repository
	hub  Hub
	// warned holds schedules whose stored interval cannot run, so the poller
	// logs each once instead of every 30 seconds.
	warned sync.Map
}

// ErrRunInProgress is a trigger for a schedule whose previous run has not
// finished. Firing anyway ran the script twice at once on every target.
var ErrRunInProgress = errors.New("the previous run of this schedule has not finished")

func NewScheduler(repo *Repository, hub Hub) *Scheduler {
	return &Scheduler{
		repo: repo,
		hub:  hub,
	}
}

func (s *Scheduler) TriggerSchedule(ctx context.Context, scheduleID, actorID string) (*ScheduledTaskRun, error) {
	schedule, err := s.repo.GetScheduleByID(ctx, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}

	script, err := s.repo.GetScriptByID(ctx, schedule.ScriptID)
	if err != nil {
		return nil, fmt.Errorf("get script: %w", err)
	}

	running, err := s.repo.HasRunningRun(ctx, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("check running run: %w", err)
	}
	if running {
		return nil, ErrRunInProgress
	}

	deviceIDs, err := s.repo.GetTargetDeviceIDs(ctx, schedule.TargetType, schedule.TargetID)
	if err != nil {
		return nil, fmt.Errorf("resolve targets: %w", err)
	}

	run, err := s.repo.CreateRun(ctx, schedule, script.ID)
	if err != nil {
		return nil, fmt.Errorf("create task run: %w", err)
	}

	// Every device run exists before any is dispatched. The rollup counts the
	// device runs it can see, so creating them inside the dispatch loop let an
	// offline first target (or a fast first report) close the run against a
	// partial set, before the remaining endpoints were even recorded.
	devRuns := make([]*ScheduledTaskDeviceRun, 0, len(deviceIDs))
	for _, devID := range deviceIDs {
		devRun, err := s.repo.CreateDeviceRun(ctx, run.ID, devID)
		if err != nil {
			log.Error().Err(err).Str("device", devID).Msg("create device run record")
			continue
		}
		devRuns = append(devRuns, devRun)
	}

	for _, devRun := range devRuns {
		devID := devRun.DeviceID

		if s.hub == nil || !s.hub.Online(devID) {
			s.failDeviceRun(ctx, devRun.ID, "device is offline")
			continue
		}

		cmdPayload := map[string]any{
			"execution_id": devRun.ID,
			"shell":        script.ScriptType,
			"command":      script.ScriptContent,
			"timeout_sec":  script.TimeoutSeconds,
			"report_url":   "/api/agent/schedules/tasks/" + devRun.ID + "/result",
		}

		env := transport.Envelope{
			Type:    transport.TypeCommand,
			ID:      devRun.ID,
			Command: "exec.run",
			Payload: cmdPayload,
		}
		envBytes, err := json.Marshal(env)
		if err != nil {
			s.failDeviceRun(ctx, devRun.ID, "encode error: "+err.Error())
			continue
		}

		if !s.hub.SendTo(devID, envBytes) {
			s.failDeviceRun(ctx, devRun.ID, "socket send failed")
		}
	}

	// The response has to be the row that was actually written, not a copy of the
	// struct built before the dispatch loop. Hand-editing the struct was how a
	// run ended up answering status="running" with a non-null completed_at, and
	// it could not survive the rollup above: a run whose every device was
	// offline is now 'failed' in the database, but the local struct had no idea
	// that had happened and would have gone back to the client claiming to still
	// be executing.
	//
	// Reading it back is also cheaper than the bug: one indexed lookup per trigger,
	// against a loop that already did a write per device.
	if len(deviceIDs) > 0 {
		return s.repo.GetRunByID(ctx, run.ID)
	}

	// No devices resolved -- an emptied group, a device that was retired.
	// Nothing ran, so the run failed and says why; it used to be a green
	// "completed" for a script that never reached anything.
	if err := s.repo.FailRun(ctx, run.ID, "no_targets: the target resolved to no active devices"); err != nil {
		return nil, fmt.Errorf("close run with no targets: %w", err)
	}
	return s.repo.GetRunByID(ctx, run.ID)
}

// failDeviceRun records a device run that failed on the server's own side --
// the device is offline, the command would not encode, or the socket refused it.
// These are the paths where the device will never report a result, so if they
// only wrote the device row the parent run stayed 'running' with a NULL
// completed_at for good: SyncRunStatus is called from the agent result handler,
// and an endpoint that never receives the command never calls it.
//
// An operator firing a script at a sleeping laptop got exactly that: every
// device row read 'failed, device is offline' and the run above them still
// claimed to be executing, forever, with no path to close it.
func (s *Scheduler) failDeviceRun(ctx context.Context, deviceRunID, reason string) {
	if err := s.repo.UpdateDeviceRunResult(ctx, deviceRunID, "failed", -1, "", reason); err != nil {
		log.Error().Err(err).Str("device_run", deviceRunID).Msg("record device run failure")
		return
	}
	if err := s.repo.SyncRunStatus(ctx, deviceRunID); err != nil {
		log.Error().Err(err).Str("device_run", deviceRunID).Msg("roll up run status after a dispatch failure")
	}
}

// StartBackgroundScheduler checks interval schedules periodically until ctx is
// cancelled.
//
// The context is not decoration. This used to be `for range ticker.C` with no
// way out and no ticker.Stop(), so it could not be stopped at shutdown, and a
// second poller could be started beside a running one -- every due interval
// schedule would then be dispatched twice per tick, producing two runs and two
// executions of the same script. main.go's cleanup closure had nothing to call.
func (s *Scheduler) StartBackgroundScheduler(ctx context.Context, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	go func() {
		// Inside the goroutine. This function returns as soon as the goroutine
		// is launched, so a defer on this line stopped the ticker immediately --
		// and a stopped ticker never delivers, so no schedule was ever polled
		// and no scheduled task ever ran, while the log and the enabled flag
		// both said otherwise.
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
				s.pollDueSchedules(pollCtx)
				cancel()
			}
		}
	}()
}

func (s *Scheduler) pollDueSchedules(ctx context.Context) {
	schedules, err := s.repo.ListSchedules(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("scheduler: list schedules")
		return
	}

	now := time.Now().UTC()
	for _, sched := range schedules {
		if !sched.IsEnabled || sched.ScheduleType != "interval" {
			continue
		}
		intervalMins, err := strconv.Atoi(sched.ScheduleExpr)
		if err != nil || intervalMins <= 0 {
			if _, seen := s.warned.LoadOrStore(sched.ID, true); !seen {
				log.Warn().Str("schedule", sched.ID).Str("expr", sched.ScheduleExpr).
					Msg("scheduler: interval is not a positive number of minutes; the schedule cannot run until it is edited")
			}
			continue
		}
		s.warned.Delete(sched.ID)

		interval := time.Duration(intervalMins) * time.Minute
		if sched.LastRunAt != nil && now.Sub(*sched.LastRunAt) < interval {
			continue
		}
		// A run still in progress is not overlapped. The schedule is not
		// claimed, so it fires on the first poll after that run finishes.
		running, err := s.repo.HasRunningRun(ctx, sched.ID)
		if err != nil {
			log.Warn().Err(err).Str("schedule", sched.ID).Msg("scheduler: check running run")
			continue
		}
		if running {
			log.Info().Str("schedule", sched.ID).Msg("scheduler: previous run still in progress; not overlapping it")
			continue
		}
		claimed, err := s.repo.ClaimDueRun(ctx, sched.ID, now, interval)
		if err != nil {
			log.Warn().Err(err).Str("schedule", sched.ID).Msg("claim due schedule")
			continue
		}
		if !claimed {
			// Another poll (or another server node) fired it first.
			continue
		}
		log.Info().Str("schedule", sched.ID).Str("name", sched.Name).Msg("triggering due interval schedule")
		if _, err := s.TriggerSchedule(ctx, sched.ID, "system-scheduler"); err != nil {
			log.Warn().Err(err).Str("schedule", sched.ID).Msg("scheduler: trigger due schedule")
		}
	}
}
