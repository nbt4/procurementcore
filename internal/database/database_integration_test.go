package database

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOpenPreservesMigrationUniqueConstraints(t *testing.T) {
	dsn := os.Getenv("PROCUREMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PROCUREMENT_TEST_DATABASE_URL to a disposable _test PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(u.Path, "/"), "_test") {
		t.Fatal("a disposable _test PostgreSQL database is required")
	}
	fixture, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL, err := fixture.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureSQL.Close()
	const testSchema = "procurement_startup_constraints_test"
	if err := fixture.Exec("DROP SCHEMA IF EXISTS " + testSchema + " CASCADE;CREATE SCHEMA " + testSchema).Error; err != nil {
		t.Fatal(err)
	}
	defer fixture.Exec("DROP SCHEMA IF EXISTS " + testSchema + " CASCADE")
	fixtureSQL.SetMaxOpenConns(1)
	if err := fixture.Exec("SET search_path TO " + testSchema).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.Exec(`CREATE TABLE proc_requisitions(id BIGSERIAL PRIMARY KEY,number VARCHAR(40) NOT NULL UNIQUE);CREATE TABLE proc_purchase_orders(id BIGSERIAL PRIMARY KEY,number VARCHAR(40) NOT NULL UNIQUE);CREATE TABLE proc_requisition_lines(id BIGSERIAL PRIMARY KEY);CREATE TABLE proc_purchase_order_lines(id BIGSERIAL PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../../migrations/006_amazon_punchout.sql", "../../migrations/007_amazon_order_confirmations.sql"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.Exec(string(source)).Error; err != nil {
			t.Fatal(err)
		}
	}
	params := u.Query()
	params.Set("search_path", testSchema)
	u.RawQuery = params.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		db, err := Open(u.String())
		if err != nil {
			t.Fatalf("startup %d with existing migration constraints: %v", attempt+1, err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"proc_requisitions_amazon_punchout_session_id_key",
		"proc_purchase_orders_amazon_punchout_session_id_key",
		"proc_amazon_punchout_sessions_token_hash_key",
		"proc_amazon_confirmation_events_payload_id_key",
	} {
		var retained int64
		if err := fixture.Raw(`SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=? AND c.conname=? AND c.contype='u'`, testSchema, name).Scan(&retained).Error; err != nil || retained != 1 {
			t.Fatalf("original unique constraint %s not retained: %d, %v", name, retained, err)
		}
	}
}
