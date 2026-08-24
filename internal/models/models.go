package models

import (
	"encoding/json"
	"time"
)

type Supplier struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:180;not null;index" json:"name"`
	Code            string    `gorm:"size:40;uniqueIndex;not null" json:"code"`
	Website         string    `gorm:"size:1000" json:"website"`
	ContactName     string    `gorm:"size:160" json:"contactName"`
	Email           string    `gorm:"size:255" json:"email"`
	Phone           string    `gorm:"size:80" json:"phone"`
	PaymentTerms    string    `gorm:"size:120" json:"paymentTerms"`
	DefaultLeadDays int       `json:"defaultLeadDays"`
	Rating          float64   `json:"rating"`
	Preferred       bool      `gorm:"index" json:"preferred"`
	Active          bool      `gorm:"default:true;index" json:"active"`
	RiskLevel       string    `gorm:"size:20;default:'low'" json:"riskLevel"`
	Notes           string    `gorm:"type:text" json:"notes"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Category struct {
	ID              uint            `gorm:"primaryKey" json:"id"`
	Name            string          `gorm:"size:160;uniqueIndex;not null" json:"name"`
	Description     string          `gorm:"type:text" json:"description"`
	ParameterSchema json.RawMessage `gorm:"type:jsonb;default:'[]'" json:"parameterSchema"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type Product struct {
	ID           uint            `gorm:"primaryKey" json:"id"`
	SKU          string          `gorm:"size:80;uniqueIndex;not null" json:"sku"`
	Name         string          `gorm:"size:240;not null;index" json:"name"`
	Description  string          `gorm:"type:text" json:"description"`
	CategoryID   *uint           `gorm:"index" json:"categoryId"`
	Category     *Category       `json:"category,omitempty"`
	Unit         string          `gorm:"size:30;default:'Stk.'" json:"unit"`
	Manufacturer string          `gorm:"size:180;index" json:"manufacturer"`
	Model        string          `gorm:"size:180" json:"model"`
	Parameters   json.RawMessage `gorm:"type:jsonb;default:'{}';index:,type:gin" json:"parameters"`
	Attributes   json.RawMessage `gorm:"type:jsonb;default:'{}';index:,type:gin" json:"attributes"`
	Active       bool            `gorm:"default:true;index" json:"active"`
	ReorderPoint float64         `json:"reorderPoint"`
	TargetStock  float64         `json:"targetStock"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Offers       []Offer         `json:"offers,omitempty"`
}

type Offer struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	ProductID       uint       `gorm:"not null;index" json:"productId"`
	SupplierID      uint       `gorm:"not null;index" json:"supplierId"`
	Supplier        *Supplier  `json:"supplier,omitempty"`
	SupplierSKU     string     `gorm:"size:120" json:"supplierSku"`
	PriceCents      int64      `gorm:"not null;index" json:"priceCents"`
	Currency        string     `gorm:"size:3;default:'EUR'" json:"currency"`
	MinimumQuantity float64    `gorm:"default:1" json:"minimumQuantity"`
	PackSize        float64    `gorm:"default:1" json:"packSize"`
	LeadDays        int        `json:"leadDays"`
	PurchaseURL     string     `gorm:"size:2000" json:"purchaseUrl"`
	ValidUntil      *time.Time `json:"validUntil"`
	Active          bool       `gorm:"default:true;index" json:"active"`
	LastCheckedAt   time.Time  `json:"lastCheckedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type PriceHistory struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	OfferID    uint      `gorm:"not null;index" json:"offerId"`
	PriceCents int64     `gorm:"not null" json:"priceCents"`
	Currency   string    `gorm:"size:3;not null" json:"currency"`
	RecordedAt time.Time `gorm:"index" json:"recordedAt"`
}

type PriceAlert struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	ProductID           uint       `gorm:"not null;index" json:"productId"`
	Product             *Product   `json:"product,omitempty"`
	TargetPriceCents    int64      `gorm:"not null" json:"targetPriceCents"`
	Currency            string     `gorm:"size:3;default:'EUR'" json:"currency"`
	Active              bool       `gorm:"default:true;index" json:"active"`
	Triggered           bool       `gorm:"default:false;index" json:"triggered"`
	TriggeredPriceCents *int64     `json:"triggeredPriceCents"`
	TriggeredOfferID    *uint      `json:"triggeredOfferId"`
	TriggeredAt         *time.Time `json:"triggeredAt"`
	CreatedBy           uint       `gorm:"not null;index" json:"createdBy"`
	CreatedByName       string     `gorm:"size:160" json:"createdByName"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type Requisition struct {
	ID                  uint              `gorm:"primaryKey" json:"id"`
	Number              string            `gorm:"size:40;uniqueIndex;not null" json:"number"`
	Title               string            `gorm:"size:240;not null" json:"title"`
	Status              string            `gorm:"size:30;default:'draft';index" json:"status"`
	RequesterID         uint              `gorm:"not null;index" json:"requesterId"`
	RequesterName       string            `gorm:"size:160" json:"requesterName"`
	CostCenter          string            `gorm:"size:80;index" json:"costCenter"`
	Justification       string            `gorm:"type:text" json:"justification"`
	NeededBy            *time.Time        `json:"neededBy"`
	EstimatedTotalCents int64             `json:"estimatedTotalCents"`
	ApprovedBy          *uint             `json:"approvedBy"`
	ApprovedByName      string            `gorm:"size:160" json:"approvedByName"`
	DecisionNote        string            `gorm:"type:text" json:"decisionNote"`
	SubmittedAt         *time.Time        `json:"submittedAt"`
	DecidedAt           *time.Time        `json:"decidedAt"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
	Lines               []RequisitionLine `json:"lines"`
}

type RequisitionLine struct {
	ID                  uint     `gorm:"primaryKey" json:"id"`
	RequisitionID       uint     `gorm:"not null;index" json:"requisitionId"`
	ProductID           *uint    `gorm:"index" json:"productId"`
	Product             *Product `json:"product,omitempty"`
	Description         string   `gorm:"size:500;not null" json:"description"`
	Quantity            float64  `gorm:"not null" json:"quantity"`
	Unit                string   `gorm:"size:30" json:"unit"`
	EstimatedPriceCents int64    `json:"estimatedPriceCents"`
	PreferredSupplierID *uint    `json:"preferredSupplierId"`
	PurchaseURL         string   `gorm:"size:2000" json:"purchaseUrl"`
}

type PurchaseOrder struct {
	ID               uint                `gorm:"primaryKey" json:"id"`
	Number           string              `gorm:"size:40;uniqueIndex;not null" json:"number"`
	SupplierID       uint                `gorm:"not null;index" json:"supplierId"`
	Supplier         *Supplier           `json:"supplier,omitempty"`
	RequisitionID    *uint               `gorm:"index" json:"requisitionId"`
	Status           string              `gorm:"size:30;default:'draft';index" json:"status"`
	Currency         string              `gorm:"size:3;default:'EUR'" json:"currency"`
	TotalCents       int64               `json:"totalCents"`
	OrderedBy        uint                `json:"orderedBy"`
	OrderedByName    string              `gorm:"size:160" json:"orderedByName"`
	OrderDate        *time.Time          `json:"orderDate"`
	ExpectedDelivery *time.Time          `json:"expectedDelivery"`
	Notes            string              `gorm:"type:text" json:"notes"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
	Lines            []PurchaseOrderLine `json:"lines"`
}

type PurchaseOrderLine struct {
	ID               uint     `gorm:"primaryKey" json:"id"`
	PurchaseOrderID  uint     `gorm:"not null;index" json:"purchaseOrderId"`
	ProductID        *uint    `gorm:"index" json:"productId"`
	Product          *Product `json:"product,omitempty"`
	Description      string   `gorm:"size:500;not null" json:"description"`
	Quantity         float64  `json:"quantity"`
	ReceivedQuantity float64  `json:"receivedQuantity"`
	Unit             string   `gorm:"size:30" json:"unit"`
	UnitPriceCents   int64    `json:"unitPriceCents"`
	PurchaseURL      string   `gorm:"size:2000" json:"purchaseUrl"`
}

type Receipt struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	PurchaseOrderID     uint      `gorm:"not null;index" json:"purchaseOrderId"`
	PurchaseOrderLineID uint      `gorm:"not null;index" json:"purchaseOrderLineId"`
	Quantity            float64   `json:"quantity"`
	ReceivedBy          uint      `json:"receivedBy"`
	ReceivedByName      string    `gorm:"size:160" json:"receivedByName"`
	Note                string    `gorm:"type:text" json:"note"`
	ReceivedAt          time.Time `json:"receivedAt"`
}

type Activity struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	EntityType string    `gorm:"size:40;index" json:"entityType"`
	EntityID   uint      `gorm:"index" json:"entityId"`
	Action     string    `gorm:"size:80" json:"action"`
	UserID     uint      `json:"userId"`
	Username   string    `gorm:"size:160" json:"username"`
	Details    string    `gorm:"type:text" json:"details"`
	CreatedAt  time.Time `gorm:"index" json:"createdAt"`
}

func (Supplier) TableName() string          { return "proc_suppliers" }
func (Category) TableName() string          { return "proc_categories" }
func (Product) TableName() string           { return "proc_products" }
func (Offer) TableName() string             { return "proc_offers" }
func (PriceHistory) TableName() string      { return "proc_price_histories" }
func (PriceAlert) TableName() string        { return "proc_price_alerts" }
func (Requisition) TableName() string       { return "proc_requisitions" }
func (RequisitionLine) TableName() string   { return "proc_requisition_lines" }
func (PurchaseOrder) TableName() string     { return "proc_purchase_orders" }
func (PurchaseOrderLine) TableName() string { return "proc_purchase_order_lines" }
func (Receipt) TableName() string           { return "proc_receipts" }
func (Activity) TableName() string          { return "proc_activities" }
