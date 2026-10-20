// Package cluster holds what several server nodes sharing one PostgreSQL need
// to agree on: which node runs the background loops, which node holds a
// device's agent socket, and how a command reaches that node.
//
// On SQLite there is only ever one node, and everything here collapses to the
// single-process behaviour the server always had.
package cluster

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// Leader elects one node to run the background loops that must not run twice:
// the scheduler, the alert evaluator, the sweeps and session purging.
//
// Leadership is a PostgreSQL session advisory lock held on one dedicated
// connection. The database releases it the moment that connection ends, so a
// leader that crashes, is killed or loses its network hands leadership to
// another node without anyone having to notice a timeout. A node that is not
// leader retries every RetryEvery.
type Leader struct {
	db         *sqlx.DB
	key        int64
	RetryEvery time.Duration
	// CheckEvery is how often the leader confirms its connection is alive. A
	// leader whose connection is gone has already lost the lock in the
	// database; this is how it finds out and stops its loops.
	CheckEvery time.Duration

	leading atomic.Bool
}

func NewLeader(d *sqlx.DB) *Leader {
	return &Leader{db: d, key: db.LockLeader, RetryEvery: 10 * time.Second, CheckEvery: 5 * time.Second}
}

// IsLeader reports whether this node currently runs the leader-only loops.
func (l *Leader) IsLeader() bool { return l.leading.Load() }

// Run blocks until ctx ends. Each time this node becomes leader it calls
// onElected with a context that is cancelled when leadership is lost, so the
// loops onElected starts stop on the same signal. onElected must start its work
// and return.
func (l *Leader) Run(ctx context.Context, onElected func(leaderCtx context.Context)) {
	if !db.IsPostgres(l.db) {
		// SQLite: the only node is the leader, for as long as it runs.
		l.leading.Store(true)
		onElected(ctx)
		<-ctx.Done()
		l.leading.Store(false)
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if l.lead(ctx, onElected) {
			continue // lost leadership; try again at once
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.RetryEvery):
		}
	}
}

// lead tries once to take the lock and, if it does, holds it until it is lost
// or ctx ends. It reports whether this node was leader at all.
func (l *Leader) lead(ctx context.Context, onElected func(context.Context)) bool {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("leader election: no database connection")
		return false
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(?)`, l.key).Scan(&got); err != nil || !got {
		_ = conn.Close()
		if err != nil {
			log.Warn().Err(err).Msg("leader election: try lock")
		}
		return false
	}

	leaderCtx, stop := context.WithCancel(ctx)
	l.leading.Store(true)
	log.Info().Msg("this node is now the leader and runs the background loops")
	onElected(leaderCtx)

	defer func() {
		stop()
		l.leading.Store(false)
		// Unlocking explicitly matters only on a clean stop; when the
		// connection is gone the database already released the lock.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(?)`, l.key)
		_ = conn.Close()
	}()

	tick := time.NewTicker(l.CheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-tick.C:
			if err := pingConn(ctx, conn); err != nil {
				log.Warn().Err(err).Msg("leader lost its database connection; stepping down")
				return true
			}
		}
	}
}

func pingConn(ctx context.Context, conn *sql.Conn) error {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.PingContext(pctx)
}
