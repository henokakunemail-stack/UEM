package db

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

func init() {
	sql.Register("postgres-rebind", &rebindDriver{})
	sqlx.BindDriver("postgres-rebind", sqlx.DOLLAR)
}

type rebindDriver struct {
	pq.Driver
}

func (d *rebindDriver) Open(name string) (driver.Conn, error) {
	c, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return &rebindConn{Conn: c}, nil
}

type rebindConn struct {
	driver.Conn
}

func rebind(q string) string {
	return sqlx.Rebind(sqlx.DOLLAR, q)
}

func (c *rebindConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(rebind(query))
}

func (c *rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, rebind(query))
	}
	return c.Conn.Prepare(rebind(query))
}

func (c *rebindConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	if exec, ok := c.Conn.(driver.Execer); ok {
		return exec.Exec(rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if exec, ok := c.Conn.(driver.ExecerContext); ok {
		return exec.ExecContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	if q, ok := c.Conn.(driver.Queryer); ok {
		return q.Query(rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.Conn.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if btx, ok := c.Conn.(driver.ConnBeginTx); ok {
		return btx.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *rebindConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *rebindConn) ResetSession(ctx context.Context) error {
	if rs, ok := c.Conn.(driver.SessionResetter); ok {
		return rs.ResetSession(ctx)
	}
	return nil
}
