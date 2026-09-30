package taskscheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
}

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

	deviceIDs, err := s.repo.GetTargetDeviceIDs(ctx, schedule.TargetType, schedule.TargetID)
	if err != nil {
		return nil, fmt.Errorf("resolve targets: %w", err)
	}

	run, err := s.repo.CreateRun(ctx, scheduleID, script.ID)
	if err != nil {
		return nil, fmt.Errorf("create task run: %w", err)
	}

	for _, devID := range deviceIDs {
		devRun, err := s.repo.CreateDeviceRun(ctx, run.ID, devID)
		if err != nil {
			log.Error().Err(err).Str("device", devID).Msg("create device run record")
			continue
		}

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

	// No devices resolved. Nothing will ever report for this run, so it is
	// finished here and now.
	if err := s.repo.CompleteRun(ctx, run.ID, "completed"); err != nil {
		return nil, fmt.Errorf("complete run with no targets: %w", err)
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

// StartBackgroundScheduler checks interval schedules periodically
func (s *Scheduler) StartBackgroundScheduler(pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	go func() {
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
			s.pollDueSchedules(ctx)
			cancel()
		}
	}()
}

func (s *Scheduler) pollDueSchedules(ctx context.Context) {
	schedules, err := s.repo.ListSchedules(ctx)
	if err != nil {
		return
	}

	now := time.Now().UTC()
	for _, sched := range schedules {
		if !sched.IsEnabled {
			continue
		}

		if sched.ScheduleType == "interval" {
			intervalMins, err := strconv.Atoi(sched.ScheduleExpr)
			if err != nil || intervalMins <= 0 {
				continue
			}

			if sched.LastRunAt == nil || now.Sub(*sched.LastRunAt) >= time.Duration(intervalMins)*time.Minute {
				log.Info().Str("schedule", sched.ID).Str("name", sched.Name).Msg("triggering due interval schedule")
				_, _ = s.TriggerSchedule(ctx, sched.ID, "system-scheduler")
			}
		}
	}
}
