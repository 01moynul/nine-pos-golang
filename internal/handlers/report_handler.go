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

// --- DATA STRUCTURES FOR VALUATION REPORT ---

// ValuationItem represents a single row in the PDF table
type ValuationItem struct {
	Name      string  `json:"name"`
	Quantity  int     `json:"quantity"`
	CostPrice float64 `json:"cost_price"`
	TotalCost float64 `json:"total_cost"`
}

// CategoryGroup represents one entire table in the PDF (e.g., "DRINKS")
type CategoryGroup struct {
	CategoryName string          `json:"category_name"`
	Items        []ValuationItem `json:"items"`
	Subtotal     float64         `json:"subtotal"`
}

// ValuationResponse is the final payload sent to React
type ValuationResponse struct {
	Categories []CategoryGroup `json:"categories"`
	GrandTotal float64         `json:"grand_total"`
}

// --- GET: /api/reports/valuation ---
// GetStockValuation calculates the total monetary value of all physical inventory
func GetStockValuation(c *gin.Context) {
	var products []models.Product

	// 1. Fetch all products from the database
	if err := database.DB.Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch inventory"})
		return
	}

	// 2. Initialize our running totals and a map to group items by Category
	var grandTotal float64
	// We use a pointer to CategoryGroup so we can easily update the subtotal in the map
	groupedMap := make(map[string]*CategoryGroup)

	// 3. Loop through every single product in the database
	for _, p := range products {
		// Safety check: If an item has no category, group it as "Uncategorized"
		catName := p.Category
		if catName == "" {
			catName = "Uncategorized"
		}

		// If this is the first time we are seeing this category, create its group
		if _, exists := groupedMap[catName]; !exists {
			groupedMap[catName] = &CategoryGroup{
				CategoryName: catName,
				Items:        []ValuationItem{},
				Subtotal:     0,
			}
		}

		// Calculate the mathematical value of this specific item's stock
		itemTotal := float64(p.StockQuantity) * p.CostPrice

		// Create the row data for this item
		valItem := ValuationItem{
			Name:      p.Name,
			Quantity:  p.StockQuantity,
			CostPrice: p.CostPrice,
			TotalCost: itemTotal, // Qty * CostPrice
		}

		// Append the item to its specific category group
		groupedMap[catName].Items = append(groupedMap[catName].Items, valItem)

		// Add the item's total to both the Category Subtotal and the Grand Total
		groupedMap[catName].Subtotal += itemTotal
		grandTotal += itemTotal
	}

	// 4. Convert the Map into a flat Array (Slice) so React can easily loop over it
	var response ValuationResponse
	response.GrandTotal = grandTotal
	for _, group := range groupedMap {
		response.Categories = append(response.Categories, *group)
	}

	// 5. Send the structured data to the frontend
	c.JSON(http.StatusOK, response)
}
