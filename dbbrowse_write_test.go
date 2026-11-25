package main

import (
	"strings"
	"testing"
)

func TestDBWritableSQLAllowsTableLevelWrites(t *testing.T) {
	ok := []string{
		"UPDATE t SET a=1 WHERE id=2",
		"INSERT INTO t (a) VALUES (1)",
		"DELETE FROM t WHERE id=2",
		"TRUNCATE TABLE t",
		"CREATE TABLE t2 (id INT PRIMARY KEY)",
		"ALTER TABLE t ADD COLUMN b INT",
		"CREATE INDEX idx_t_a ON t (a)",
		"DROP TABLE IF EXISTS t2",
		"DROP INDEX idx_t_a ON t",
		"RENAME TABLE t TO t3",
	}
	for _, sql := range ok {
		if _, err := dbWritableSQL(sql); err != nil {
			t.Errorf("dbWritableSQL(%q) = error %v, want allowed", sql, err)
		}
	}
}

func TestDBWritableSQLRejectsDatabaseAndArbitraryDDL(t *testing.T) {
	bad := []string{
		"DROP DATABASE app",
		"DROP /* bypass */ DATABASE app",
		"DROP SCHEMA app",
		"CREATE DATABASE app",
		"GRANT ALL ON *.* TO u",
		"SET GLOBAL max_connections=100",
		"SELECT 1",
		"UPDATE t SET a=1; DELETE FROM t",
	}
	for _, sql := range bad {
		if _, err := dbWritableSQL(sql); err == nil {
			t.Errorf("dbWritableSQL(%q) = nil, want rejected", sql)
		}
	}
}

func TestBuildCellUpdateSQLUsesAllPrimaryKeyParts(t *testing.T) {
	pk := []dbKeyPart{{Column: "tenant", Value: strp("a")}, {Column: "id", Value: strp("7")}}
	sql, err := buildCellUpdateSQL("app", "orders", "note", pk)
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE `app`.`orders` SET `note`=? WHERE `tenant`=? AND `id`=? LIMIT 1"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestBuildCellUpdateSQLRejectsUnsafeTargets(t *testing.T) {
	if _, err := buildCellUpdateSQL("app", "t", "a", nil); err == nil {
		t.Fatal("empty primary key should be rejected")
	}
	if _, err := buildCellUpdateSQL("app", "t", "id", []dbKeyPart{{Column: "id", Value: strp("1")}}); err == nil {
		t.Fatal("editing a primary-key column should be rejected")
	}
	if _, err := buildCellUpdateSQL("app", "t", "a", []dbKeyPart{{Column: "id"}}); err == nil {
		t.Fatal("NULL primary-key part should be rejected")
	}
	if _, err := buildCellUpdateSQL("app", "t", "a", []dbKeyPart{{Column: "`id`", Value: strp("1")}}); err == nil {
		t.Fatal("invalid identifier should be rejected")
	}
}

func TestDBOneStatementRejectsMultipleStatements(t *testing.T) {
	for _, sql := range []string{"SELECT 1; SELECT 2", "UPDATE t SET a=1; DELETE FROM t"} {
		if _, err := dbOneStatement(sql); err == nil || !strings.Contains(err.Error(), "一条") {
			t.Errorf("dbOneStatement(%q) = %v, want one-statement error", sql, err)
		}
	}
	if _, err := dbOneStatement("SELECT 1;"); err != nil {
		t.Errorf("trailing semicolon should be allowed: %v", err)
	}
}

func strp(v string) *string { return &v }
