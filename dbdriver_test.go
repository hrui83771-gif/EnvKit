package main

import (
	"strings"
	"testing"
)

func TestMySQLConnConfigIPv6(t *testing.T) {
	c, err := (mysqlConnParams{Host: "::1", Port: 3307, User: "root", Pass: "p", DB: "shop"}).config()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "[::1]:3307" {
		t.Fatalf("addr = %q", c.Addr)
	}
	if c.DBName != "shop" || c.User != "root" || c.Passwd != "p" {
		t.Fatalf("config = %+v", c)
	}
	if !strings.Contains(c.FormatDSN(), "charset=utf8mb4") {
		t.Fatalf("dsn missing charset: %s", c.FormatDSN())
	}
}

func TestMySQLConnConfigRejectsBadDBName(t *testing.T) {
	if _, err := (mysqlConnParams{DB: "bad`name"}).config(); err == nil {
		t.Fatal("backtick in db name should be rejected")
	}
}
