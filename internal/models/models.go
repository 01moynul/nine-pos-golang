package models

import (
	"time"
)

// User - The person interacting with the system
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:50" json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

// Product - The Inventory (Upgraded for Phase 1 & 3)
type Product struct {
	ID              uint    `gorm:"primaryKey" json:"id"`
	SKU             string  `gorm:"uniqueIndex;size:100" json:"sku"` // Required for Phase 2.1 (Barcodes)
	Name            string  `json:"name"`
	Price           float64 `json:"price"`      // SellPriceRM
	CostPrice       float64 `json:"cost_price"` // BuyPriceRM (Task 3.2 Profit Tracking)
	Category        string  `json:"category"`
	StockQuantity   int     `json:"stock_quantity"`    // Acts as StockAvailable
	StockReserved   int     `json:"stock_reserved"`    // Required for Task 3.1 (Omnichannel Holds)
	IsSSTApplicable bool    `json:"is_sst_applicable"` // Required for Task 1.3 (Tax Config)
	ImageURL        string  `json:"image_url"`
}

// ComboComponent - Required for Task 3.3 (Bundle Engine)
type ComboComponent struct {
	ID                 uint `gorm:"primaryKey" json:"id"`
	BundleProductID    uint `json:"bundle_product_id"`    // e.g., The Promo Basket
	ComponentProductID uint `json:"component_product_id"` // e.g., The Can of Beans inside it
	Quantity           int  `json:"quantity"`             // How many beans in the basket
}

// Sale - The Transaction Header (Upgraded for LHDN & Phase 7)
type Sale struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	ReceiptID        string     `gorm:"uniqueIndex;size:50" json:"receipt_id"` // Task 7.1 (e.g., RCPT-2026-XYZ)
	UserID           uint       `json:"user_id"`
	TotalAmount      float64    `json:"total_amount"`
	Status           string     `json:"status"`
	SaleTime         time.Time  `json:"sale_time"`
	LHDNValidationID string     `json:"lhdn_validation_id"` // Task 2.2 (Mandatory Tax Compliance)
	LHDNQRCodeURL    string     `json:"lhdn_qr_code_url"`   // Task 2.2
	Items            []SaleItem `gorm:"foreignKey:SaleID" json:"items"`
}

// SaleItem - The specific items in a cart
type SaleItem struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	SaleID      uint    `json:"sale_id"`
	ProductID   uint    `json:"product_id"`
	Product     Product `json:"product"`
	Quantity    int     `json:"quantity"`
	BuyPriceRM  float64 `json:"buy_price_rm"`  // Snapshot of Cost at checkout (Task 3.2)
	PriceAtSale float64 `json:"price_at_sale"` // Snapshot of Sell Price at checkout
}

// AuditLog - Required for Task 6.2 (Immutable Audits)
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `json:"user_id"`
	Action    string    `json:"action"` // e.g., "PRICE_OVERRIDE", "REFUND"
	Details   string    `json:"details"`
	Timestamp time.Time `json:"timestamp"`
}
