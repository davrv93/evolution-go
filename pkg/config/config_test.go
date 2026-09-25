package config

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

type poolTestConnector struct{}

func (poolTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &poolTestConn{}, nil
}

func (poolTestConnector) Driver() driver.Driver { return poolTestDriver{} }

type poolTestDriver struct{}

func (poolTestDriver) Open(string) (driver.Conn, error) { return &poolTestConn{}, nil }

type poolTestConn struct{}

func (*poolTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*poolTestConn) Close() error                        { return nil }
func (*poolTestConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func TestConfigurePostgresPoolBoundsOpenAndIdleConnections(t *testing.T) {
	db := sql.OpenDB(poolTestConnector{})
	defer db.Close()

	ConfigurePostgresPool(db)

	stats := db.Stats()
	if stats.MaxOpenConnections != postgresMaxOpenConns {
		t.Fatalf("max open connections = %d, want %d", stats.MaxOpenConnections, postgresMaxOpenConns)
	}

	connections := make([]*sql.Conn, postgresMaxIdleConns+1)
	for i := range connections {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("acquire connection %d: %v", i, err)
		}
		connections[i] = conn
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("release connection: %v", err)
		}
	}

	stats = db.Stats()
	if stats.Idle != postgresMaxIdleConns {
		t.Fatalf("idle connections = %d, want %d", stats.Idle, postgresMaxIdleConns)
	}
}
