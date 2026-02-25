package models

import (
	"time"
)

// User - The person interacting with the system (and the AI)
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:50" json:"username"`
	PasswordHash string    `json:"-"`    // Never return this in JSON
	Role         string    `json:"role"` // 'admin', 'manager', 'cashier'
	CreatedAt    time.Time `json:"created_at"`
}

// Product - The Inventory
type Product struct {
	ID            uint    `gorm:"primaryKey" json:"id"`
	Name          string  `json:"name"`
	Price         float64 `json:"price"`
	Category      string  `json:"category"`
	StockQuantity int     `json:"stock_quantity"`
	ImageURL      string  `json:"image_url"`
}

// Sale - The Transaction Header
type Sale struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      uint       `json:"user_id"` // Who processed it
	TotalAmount float64    `json:"total_amount"`
	Status      string     `json:"status"` // 'completed', 'held', 'cancelled' <-- UPGRADE
	SaleTime    time.Time  `json:"sale_time"`
	Items       []SaleItem `gorm:"foreignKey:SaleID" json:"items"`
}

// SaleItem - The specific items in a cart
type SaleItem struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	SaleID      uint    `json:"sale_id"`
	ProductID   uint    `json:"product_id"`
	Product     Product `json:"product"` // Preload product details
	Quantity    int     `json:"quantity"`
	PriceAtSale float64 `json:"price_at_sale"` // Snapshot of price at time of sale
}
