package conditions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestReadSymReportsAFailedTableCheck(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE sym_node_identity (node_id VARCHAR)",
		"CREATE TABLE sym_node_host (node_id VARCHAR, host_name VARCHAR, heartbeat_time TIMESTAMP, timezone_offset VARCHAR)",
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	fail := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "sqlite_master") {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM injected_failure")
			tx.Statement.Vars = nil
		}
	}
	if err := db.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if _, err := readSym(context.Background(), db, time.Now()); err == nil || errors.Is(err, ErrNoSym) {
		t.Fatalf("a failed table check reads as no SymmetricDS tables: %v", err)
	}
}
