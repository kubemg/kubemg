package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"

	"github.com/kubemg/kubemg/backend/pkg/config"
)

// poolTestDriver hands out connections to nothing, so Open's pool settings can
// be read back through database/sql's own accounting without a server.
const poolTestDriver = "kubemg-pool-test"

var registerPoolTestDriver sync.Once

type nullDriver struct{}

func (nullDriver) Open(string) (driver.Conn, error) { return nullConn{}, nil }

type nullConn struct{}

func (nullConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no statements") }
func (nullConn) Close() error                        { return nil }
func (nullConn) Begin() (driver.Tx, error)           { return nil, errors.New("no transactions") }

func openPoolTest(t *testing.T, cfg config.DB) *sql.DB {
	t.Helper()
	registerPoolTestDriver.Do(func() { sql.Register(poolTestDriver, nullDriver{}) })
	gdb, err := open(postgres.New(postgres.Config{DriverName: poolTestDriver}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func TestOpenCapsTheConnectionsItHolds(t *testing.T) {
	// Three idle rather than two, because two is database/sql's own default.
	pool := openPoolTest(t, config.DB{MaxOpenConns: 5, MaxIdleConns: 3, ConnMaxLifetime: time.Hour})

	if got := pool.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("max open connections = %d, want 5", got)
	}

	// Five in use at once, then all handed back: three are kept for the next
	// request and the other two are closed.
	ctx := context.Background()
	conns := make([]*sql.Conn, 5)
	for i := range conns {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = conn
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	stats := pool.Stats()
	if stats.Idle != 3 || stats.MaxIdleClosed != 2 {
		t.Fatalf("idle = %d, closed as surplus idle = %d; want 3 and 2", stats.Idle, stats.MaxIdleClosed)
	}
}

func TestOpenRetiresConnectionsPastTheirLifetime(t *testing.T) {
	for _, tc := range []struct {
		lifetime time.Duration
		retired  bool
	}{
		{lifetime: time.Millisecond, retired: true},
		{lifetime: time.Hour, retired: false},
	} {
		pool := openPoolTest(t, config.DB{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: tc.lifetime})

		ctx := context.Background()
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
		_ = conn.Close()

		if retired := pool.Stats().MaxLifetimeClosed > 0; retired != tc.retired {
			t.Fatalf("lifetime %s: retired = %t, want %t", tc.lifetime, retired, tc.retired)
		}
	}
}
