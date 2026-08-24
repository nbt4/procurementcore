package config

import "testing"

func TestLoadRequiresBothAdamHallCredentials(t *testing.T) {
	t.Setenv("DB_PASSWORD", "database-secret")
	t.Setenv("CORES_JWT_SECRET", "jwt-secret")
	t.Setenv("ADAMHALL_USERNAME", "buyer@example.com")
	t.Setenv("ADAMHALL_PASSWORD", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected incomplete Adam Hall configuration to fail")
	}
}

func TestLoadAcceptsAdamHallCredentials(t *testing.T) {
	t.Setenv("DB_PASSWORD", "database-secret")
	t.Setenv("CORES_JWT_SECRET", "jwt-secret")
	t.Setenv("ADAMHALL_USERNAME", "buyer@example.com")
	t.Setenv("ADAMHALL_PASSWORD", "shop-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdamHallUsername != "buyer@example.com" || cfg.AdamHallPassword != "shop-secret" {
		t.Fatal("Adam Hall credentials were not loaded")
	}
}
