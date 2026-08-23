package database

import (
	"fmt"
	"time"

	"procurementcore/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	if err := db.AutoMigrate(
		&models.Supplier{}, &models.Category{}, &models.Product{}, &models.Offer{},
		&models.PriceHistory{}, &models.PriceAlert{}, &models.Requisition{},
		&models.RequisitionLine{}, &models.PurchaseOrder{}, &models.PurchaseOrderLine{},
		&models.Receipt{}, &models.Activity{},
	); err != nil {
		return nil, fmt.Errorf("migrate procurement schema: %w", err)
	}
	return db, nil
}
