package handlers

import (
	"net/http"
	"time"

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

// --- GET: /api/reports/valuation/history ---
// GetHistoricalValuation calculates inventory value based on the StockLedger at a specific past date
func GetHistoricalValuation(c *gin.Context) {
	// 1. Get the requested date from the URL (e.g., ?date=2026-02-27)
	dateStr := c.Query("date")
	if dateStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Date parameter is required (YYYY-MM-DD)"})
		return
	}

	// 2. Parse the date and set the clock to the very end of that day (23:59:59)
	targetDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format. Use YYYY-MM-DD"})
		return
	}
	endOfDay := targetDate.Add(24*time.Hour - time.Second)

	// 3. Fetch all products so we know what to look for
	var products []models.Product
	if err := database.DB.Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch inventory"})
		return
	}

	var grandTotal float64
	groupedMap := make(map[string]*CategoryGroup)

	// 4. Loop through every product to find its historical stock
	for _, p := range products {
		var lastLedger models.StockLedger

		// THE TIME MACHINE: Find the single most recent ledger entry for this product that happened ON or BEFORE the target date.
		err := database.DB.Where("product_id = ? AND created_at <= ?", p.ID, endOfDay).
			Order("created_at desc").
			First(&lastLedger).Error

		historicalStock := 0
		if err == nil {
			// We found a ledger entry! This was the exact stock balance on that day.
			historicalStock = lastLedger.Balance
		} else {
			// No ledger entries exist before this date. This means the item wasn't in the store yet (Stock = 0).
			continue
		}

		// If stock was 0 on that date, skip it to keep the report clean
		if historicalStock <= 0 {
			continue
		}

		// --- Apply the exact same grouping math as the live valuation ---
		catName := p.Category
		if catName == "" {
			catName = "Uncategorized"
		}

		if _, exists := groupedMap[catName]; !exists {
			groupedMap[catName] = &CategoryGroup{
				CategoryName: catName,
				Items:        []ValuationItem{},
				Subtotal:     0,
			}
		}

		itemTotal := float64(historicalStock) * p.CostPrice

		valItem := ValuationItem{
			Name:      p.Name,
			Quantity:  historicalStock,
			CostPrice: p.CostPrice,
			TotalCost: itemTotal,
		}

		groupedMap[catName].Items = append(groupedMap[catName].Items, valItem)
		groupedMap[catName].Subtotal += itemTotal
		grandTotal += itemTotal
	}

	// 5. Package and send to React
	var response ValuationResponse
	response.GrandTotal = grandTotal
	for _, group := range groupedMap {
		response.Categories = append(response.Categories, *group)
	}

	c.JSON(http.StatusOK, response)
}
