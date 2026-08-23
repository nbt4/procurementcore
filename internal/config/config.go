package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port         string
	DatabaseDSN  string
	DashboardURL string
	CookieDomain string
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
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("DB_HOST", "localhost"), env("DB_PORT", "5432"), env("DB_USER", "rentalcore"),
		dbPassword, env("DB_NAME", "rentalcore"), env("DB_SSLMODE", "disable"))
	return Config{
		Port:         port,
		DatabaseDSN:  dsn,
		DashboardURL: env("DASHBOARD_URL", "http://localhost:8080"),
		CookieDomain: os.Getenv("COOKIE_DOMAIN"),
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
