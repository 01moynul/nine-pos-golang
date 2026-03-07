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

// Product - The Inventory (Upgraded for Phase 1 & 3 & Scale Integration)
type Product struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	SKU             string    `gorm:"uniqueIndex;size:100" json:"sku"`
	Name            string    `json:"name"`
	Price           float64   `json:"price"`
	CostPrice       float64   `json:"cost_price"`
	Category        string    `json:"category"`
	StockQuantity   float64   `json:"stock_quantity"` // UPGRADED: Float64 for KG/Grams
	StockReserved   float64   `json:"stock_reserved"` // UPGRADED: Float64 for KG/Grams
	IsSSTApplicable bool      `json:"is_sst_applicable"`
	IsWeighable     bool      `json:"is_weighable"` // NEW: Scale Integration Flag
	ImageURL        string    `json:"image_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StockLedger - Enterprise Audit Trail for Inventory Management
type StockLedger struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ProductID    uint      `json:"product_id"`
	Product      Product   `json:"-"`
	ChangeAmount float64   `json:"change_amount"` // UPGRADED: e.g., +50.5 (Restock) or -1.25 (Sale)
	Balance      float64   `json:"balance"`       // UPGRADED: Float64
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}

// ComboComponent - Required for Task 3.3 (Bundle Engine)
type ComboComponent struct {
	ID                 uint    `gorm:"primaryKey" json:"id"`
	BundleProductID    uint    `json:"bundle_product_id"`
	ComponentProductID uint    `json:"component_product_id"`
	Quantity           float64 `json:"quantity"` // UPGRADED: Float64
}

// Sale - The Transaction Header
type Sale struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	ReceiptID        string     `gorm:"uniqueIndex;size:50" json:"receipt_id"`
	UserID           uint       `json:"user_id"`
	TotalAmount      float64    `json:"total_amount"`
	Status           string     `json:"status"`
	SaleTime         time.Time  `json:"sale_time"`
	LHDNValidationID string     `json:"lhdn_validation_id"`
	LHDNQRCodeURL    string     `json:"lhdn_qr_code_url"`
	SecurityVideoURL string     `json:"security_video_url"`
	Items            []SaleItem `gorm:"foreignKey:SaleID" json:"items"`
}

// SaleItem - The specific items in a cart
type SaleItem struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	SaleID      uint    `json:"sale_id"`
	ProductID   uint    `json:"product_id"`
	Product     Product `json:"product"`
	Quantity    float64 `json:"quantity"` // UPGRADED: Float64 for 1.5kg
	BuyPriceRM  float64 `json:"buy_price_rm"`
	PriceAtSale float64 `json:"price_at_sale"`
}

// AuditLog - Required for Task 6.2 (Immutable Audits)
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `json:"user_id"`
	Action    string    `json:"action"`
	Details   string    `json:"details"`
	Timestamp time.Time `json:"timestamp"`
}

// SystemLicense - Required for Phase 6 DRM Engine
type SystemLicense struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	LicenseKey     string    `gorm:"uniqueIndex;size:255" json:"license_key"`
	ExpirationDate time.Time `json:"expiration_date"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VoidedTransaction - Stores data for abandoned or completely cleared carts
type VoidedTransaction struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	SessionID        string    `json:"session_id"`
	UserID           uint      `json:"user_id"`
	TotalValueLost   float64   `json:"total_value_lost"`
	ItemsInCart      string    `json:"items_in_cart"`
	Reason           string    `json:"reason"`
	SecurityVideoURL string    `json:"security_video_url"`
	Timestamp        time.Time `json:"timestamp"`
}

// SuspiciousActivityLog - Stores lightweight pings for partial line voids
type SuspiciousActivityLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID string    `json:"session_id"`
	UserID    uint      `json:"user_id"`
	Action    string    `json:"action"`
	ItemName  string    `json:"item_name"`
	Timestamp time.Time `json:"timestamp"`
}
