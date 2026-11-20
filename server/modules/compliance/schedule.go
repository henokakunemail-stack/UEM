package compliance

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// ScheduleTick is how often the monthly schedule checks for a missing report.
const ScheduleTick = time.Hour

// startOfMonth is midnight on the first of now's month, in report time.
func startOfMonth(now time.Time) time.Time {
	t := now.In(reportZone)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, reportZone)
}

// PreviousPeriod is the calendar month before now, as YYYY-MM in report time.
func PreviousPeriod(now time.Time) string {
	return startOfMonth(now).AddDate(0, -1, 0).Format("2006-01")
}

// RunMonthly generates, for each profile with monthly reports on, the report
// for last month if it does not exist yet. It is idempotent, so a restart or a
// leader change mid-month generates nothing twice.
func (g *Generator) RunMonthly(ctx context.Context) (int, error) {
	profiles, err := g.Repo.ListProfiles(ctx)
	if err != nil {
		return 0, err
	}
	now := g.now()
	period := PreviousPeriod(now)
	periodEnd := startOfMonth(now)
	made := 0
	for _, p := range profiles {
		if !p.MonthlyReport {
			continue
		}
		// A profile created after the period ended has nothing to say about
		// it: on a fresh install the first run would otherwise publish a
		// report labelled last month from data read today.
		if !p.CreatedAt.Before(periodEnd) {
			continue
		}
		done, err := g.Repo.hasScheduled(ctx, p.ID, period)
		if err != nil {
			return made, err
		}
		if done {
			continue
		}
		rep, err := g.Generate(ctx, Request{ProfileID: p.ID, Kind: KindScheduled, Period: period,
			ActorType: "system", ActorName: "Sistem (jadwal bulanan)"})
		if err != nil {
			log.Error().Err(err).Str("profile", p.Name).Str("period", period).Msg("monthly compliance report failed")
			continue
		}
		made++
		log.Info().Str("report", rep.ID).Str("profile", p.Name).Str("period", period).Msg("monthly compliance report generated")
	}
	return made, nil
}

// StartMonthly runs RunMonthly now and every tick until ctx ends. Run it on
// the leader only.
func (g *Generator) StartMonthly(ctx context.Context, tick time.Duration) {
	run := func() {
		if _, err := g.RunMonthly(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("monthly compliance schedule")
		}
	}
	run()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
