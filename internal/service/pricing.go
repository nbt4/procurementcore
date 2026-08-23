package service

import (
	"time"

	"procurementcore/internal/models"

	"gorm.io/gorm"
)

func RecordPriceAndEvaluateAlerts(tx *gorm.DB, offer *models.Offer) error {
	history := models.PriceHistory{
		OfferID: offer.ID, PriceCents: offer.PriceCents, Currency: offer.Currency, RecordedAt: time.Now(),
	}
	if err := tx.Create(&history).Error; err != nil {
		return err
	}
	var alerts []models.PriceAlert
	if err := tx.Where("product_id = ? AND active = ? AND currency = ? AND target_price_cents >= ?",
		offer.ProductID, true, offer.Currency, offer.PriceCents).Find(&alerts).Error; err != nil {
		return err
	}
	now := time.Now()
	for i := range alerts {
		alerts[i].Triggered = true
		alerts[i].TriggeredAt = &now
		alerts[i].TriggeredPriceCents = &offer.PriceCents
		alerts[i].TriggeredOfferID = &offer.ID
		if err := tx.Save(&alerts[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func RequisitionTotal(lines []models.RequisitionLine) int64 {
	var total int64
	for _, line := range lines {
		total += int64(line.Quantity * float64(line.EstimatedPriceCents))
	}
	return total
}

func PurchaseOrderTotal(lines []models.PurchaseOrderLine) int64 {
	var total int64
	for _, line := range lines {
		total += int64(line.Quantity * float64(line.UnitPriceCents))
	}
	return total
}
