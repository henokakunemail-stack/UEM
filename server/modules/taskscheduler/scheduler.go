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

	now := time.Now().UTC()
	for _, devID := range deviceIDs {
		devRun, err := s.repo.CreateDeviceRun(ctx, run.ID, devID)
		if err != nil {
			log.Error().Err(err).Str("device", devID).Msg("create device run record")
			continue
		}

		if s.hub == nil || !s.hub.Online(devID) {
			_ = s.repo.UpdateDeviceRunResult(ctx, devRun.ID, "failed", -1, "", "device is offline")
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
			_ = s.repo.UpdateDeviceRunResult(ctx, devRun.ID, "failed", -1, "", "encode error: "+err.Error())
			continue
		}

		if !s.hub.SendTo(devID, envBytes) {
			_ = s.repo.UpdateDeviceRunResult(ctx, devRun.ID, "failed", -1, "", "socket send failed")
		}
	}

	// If no devices or all resolved, mark completed
	if len(deviceIDs) == 0 {
		_ = s.repo.CompleteRun(ctx, run.ID, "completed")
		// The struct is what the operator's client receives, and it is built
		// before the branch above runs. Stamping completed_at on it without
		// also setting the status produced a response that said
		// status="running" alongside a non-null completed_at -- a run that
		// claims to still be executing and also to have finished. The
		// persisted row was already correct, which is what made the two
		// disagree: the API and the database told the operator different
		// things about the same run.
		run.Status = "completed"
		run.CompletedAt = &now
		return run, nil
	}

	// Otherwise the run is genuinely still executing, and the two fields have
	// to agree about that: running with a null completed_at.
	run.CompletedAt = nil
	return run, nil
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
