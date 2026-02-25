package handlers

import (
	"net/http"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
)

// ReportData defines the shape of our analytics response
type ReportData struct {
	TotalRevenue float64 `json:"total_revenue"`
	TotalOrders  int64   `json:"total_orders"`
	TopSelling   []struct {
		ProductName string  `json:"product_name"`
		Sold        int     `json:"sold"`
		Revenue     float64 `json:"revenue"`
	} `json:"top_selling"`
	// --- NEW: Include Recent Transactions ---
	RecentSales []models.Sale `json:"recent_sales"`
}

// --- GET: /api/reports ---
func GetSalesReport(c *gin.Context) {
	var data ReportData

	// 1. Calculate Total Revenue (All time)
	err := database.DB.Model(&models.Sale{}).
		Select("COALESCE(SUM(total_amount), 0)").
		Scan(&data.TotalRevenue).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to calculate revenue"})
		return
	}

	// 2. Count Total Orders
	err = database.DB.Model(&models.Sale{}).Count(&data.TotalOrders).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count orders"})
		return
	}

	// 3. Find Top 5 Best Sellers
	err = database.DB.Table("sale_items").
		Select("products.name as product_name, SUM(sale_items.quantity) as sold, SUM(sale_items.quantity * sale_items.price_at_sale) as revenue").
		Joins("JOIN products ON sale_items.product_id = products.id").
		Group("products.name").
		Order("sold desc").
		Limit(5).
		Scan(&data.TopSelling).Error

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top selling items"})
		return
	}

	// 4. --- NEW: Fetch Recent Transactions ---
	// We get the last 10 sales, ordered by newest first
	err = database.DB.Order("sale_time desc").Limit(10).Find(&data.RecentSales).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch recent sales"})
		return
	}

	c.JSON(http.StatusOK, data)
}
