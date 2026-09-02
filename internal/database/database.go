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
	// This suite-wide mapping table is also initialized by WarehouseCore. Keep
	// its DDL identical and outside GORM's constraint-name reconciliation so
	// either service can safely start first.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS core_product_links (
		id BIGSERIAL PRIMARY KEY,
		procurement_product_id BIGINT NOT NULL UNIQUE,
		warehouse_product_id BIGINT NOT NULL UNIQUE,
		link_method VARCHAR(24) NOT NULL DEFAULT 'manual',
		linked_by BIGINT NOT NULL DEFAULT 0,
		linked_by_name VARCHAR(160) NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		return nil, fmt.Errorf("migrate core product links: %w", err)
	}
	return db, nil
}
