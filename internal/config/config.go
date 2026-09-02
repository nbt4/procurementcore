package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port             string
	DatabaseDSN      string
	DashboardURL     string
	WarehouseURL     string
	CookieDomain     string
	AdamHallUsername string
	AdamHallPassword string
}

func Load() (Config, error) {
	port := env("PORT", "8084")
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		return Config{}, fmt.Errorf("DB_PASSWORD is required")
	}
	if os.Getenv("CORES_JWT_SECRET") == "" {
		return Config{}, fmt.Errorf("CORES_JWT_SECRET is required")
	}
	adamHallUsername := os.Getenv("ADAMHALL_USERNAME")
	adamHallPassword := os.Getenv("ADAMHALL_PASSWORD")
	if (adamHallUsername == "") != (adamHallPassword == "") {
		return Config{}, fmt.Errorf("ADAMHALL_USERNAME and ADAMHALL_PASSWORD must be set together")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("DB_HOST", "localhost"), env("DB_PORT", "5432"), env("DB_USER", "rentalcore"),
		dbPassword, env("DB_NAME", "rentalcore"), env("DB_SSLMODE", "disable"))
	return Config{
		Port:             port,
		DatabaseDSN:      dsn,
		DashboardURL:     env("DASHBOARD_URL", "http://localhost:8080"),
		WarehouseURL:     env("WAREHOUSECORE_PUBLIC_URL", "http://localhost:8082"),
		CookieDomain:     os.Getenv("COOKIE_DOMAIN"),
		AdamHallUsername: adamHallUsername,
		AdamHallPassword: adamHallPassword,
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
