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
	TotalProfit  float64 `json:"total_profit"` // Fixes the NaN issue
	TotalOrders  int64   `json:"total_orders"`
	TopSelling   []struct {
		ProductName string  `json:"product_name"`
		Sold        int     `json:"sold"`
		Revenue     float64 `json:"revenue"`
		Profit      float64 `json:"profit"`
	} `json:"top_selling"`
	RecentSales []models.Sale              `json:"recent_sales"`
	VoidedSales []models.VoidedTransaction `json:"voided_sales"` // NEW: Task 2.4 Security Audits
}

// --- GET: /api/reports ---
func GetSalesReport(c *gin.Context) {
	var data ReportData

	// --- 1. DYNAMIC TIME FILTER LOGIC ---
	timeframe := c.Query("timeframe")     // e.g., "today", "7days", "30days", "custom"
	customStart := c.Query("customStart") // e.g., "2026-03-07T11:00"
	customEnd := c.Query("customEnd")     // e.g., "2026-03-07T17:00"

	now := time.Now()
	var startTime time.Time
	var endTime time.Time // NEW: Upper bound for our shift filter

	if timeframe == "custom" && customStart != "" && customEnd != "" {
		// Parse the HTML5 datetime-local format
		parsedStart, errStart := time.Parse("2006-01-02T15:04", customStart)
		if errStart == nil {
			startTime = parsedStart
		}

		parsedEnd, errEnd := time.Parse("2006-01-02T15:04", customEnd)
		if errEnd == nil {
			endTime = parsedEnd
		}
	} else {
		switch timeframe {
		case "today":
			startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		case "7days":
			startTime = now.AddDate(0, 0, -7)
		case "30days":
			startTime = now.AddDate(0, 0, -30)
		default:
			startTime = time.Time{} // "all" time
		}
	}

	// --- 2. REVENUE & PROFIT (Filtered) ---
	salesQuery := database.DB.Table("sale_items").
		Joins("JOIN sales ON sale_items.sale_id = sales.id").
		Where("sales.status = ?", "completed")

	if !startTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time <= ?", endTime)
	} // NEW

	row := salesQuery.
		Select("COALESCE(SUM(sale_items.quantity * sale_items.price_at_sale), 0), COALESCE(SUM(sale_items.quantity * (sale_items.price_at_sale - sale_items.buy_price_rm)), 0)").
		Row()

	if err := row.Scan(&data.TotalRevenue, &data.TotalProfit); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to calculate revenue and profit"})
		return
	}

	// --- 3. TOTAL ORDERS (Filtered) ---
	ordersQuery := database.DB.Model(&models.Sale{}).Where("status = ?", "completed")
	if !startTime.IsZero() {
		ordersQuery = ordersQuery.Where("sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		ordersQuery = ordersQuery.Where("sale_time <= ?", endTime)
	} // NEW

	if err := ordersQuery.Count(&data.TotalOrders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count orders"})
		return
	}

	// --- 4. TOP SELLING ITEMS (Filtered) ---
	topSellingQuery := database.DB.Table("sale_items").
		Select("products.name as product_name, SUM(sale_items.quantity) as sold, SUM(sale_items.quantity * sale_items.price_at_sale) as revenue, SUM(sale_items.quantity * (sale_items.price_at_sale - sale_items.buy_price_rm)) as profit").
		Joins("JOIN products ON sale_items.product_id = products.id").
		Joins("JOIN sales ON sale_items.sale_id = sales.id").
		Where("sales.status = ?", "completed")

	if !startTime.IsZero() {
		topSellingQuery = topSellingQuery.Where("sales.sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		topSellingQuery = topSellingQuery.Where("sales.sale_time <= ?", endTime)
	} // NEW

	err := topSellingQuery.Group("products.name").Order("sold desc").Limit(5).Scan(&data.TopSelling).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top selling items"})
		return
	}

	// --- 5. RECENT SALES & VOIDED (Filtered) ---
	recentSalesQuery := database.DB.Preload("Items").Preload("Items.Product").Order("sale_time desc").Limit(10)
	if !startTime.IsZero() {
		recentSalesQuery = recentSalesQuery.Where("sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		recentSalesQuery = recentSalesQuery.Where("sale_time <= ?", endTime)
	} // NEW
	recentSalesQuery.Find(&data.RecentSales)

	voidedQuery := database.DB.Order("timestamp desc").Limit(10)
	if !startTime.IsZero() {
		voidedQuery = voidedQuery.Where("timestamp >= ?", startTime)
	}
	if !endTime.IsZero() {
		voidedQuery = voidedQuery.Where("timestamp <= ?", endTime)
	} // NEW
	voidedQuery.Find(&data.VoidedSales)

	// Send data to frontend
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
// GetHistoricalValuation calculates the value of NEW stock received on a specific date, with optional time filtering
func GetHistoricalValuation(c *gin.Context) {
	dateStr := c.Query("date")
	if dateStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Date parameter is required"})
		return
	}

	// 1. Parse the base date
	targetDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid date format"})
		return
	}

	// 2. Set default time window (entire day)
	startOfDay := targetDate
	endOfDay := targetDate.Add(24*time.Hour - time.Second)

	// 3. --- NEW: Apply Time Filters if provided ---
	startTimeStr := c.Query("startTime") // e.g., "09:00"
	endTimeStr := c.Query("endTime")     // e.g., "10:00"

	if startTimeStr != "" {
		parsedStart, err := time.Parse("15:04", startTimeStr)
		if err == nil {
			// Combine the target date with the specific start time
			startOfDay = time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), parsedStart.Hour(), parsedStart.Minute(), 0, 0, targetDate.Location())
		}
	}

	if endTimeStr != "" {
		parsedEnd, err := time.Parse("15:04", endTimeStr)
		if err == nil {
			// Combine the target date with the specific end time (and add 59 seconds to include the whole minute)
			endOfDay = time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), parsedEnd.Hour(), parsedEnd.Minute(), 59, 0, targetDate.Location())
		}
	}
	// ----------------------------------------------

	var products []models.Product
	if err := database.DB.Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch inventory"})
		return
	}

	var grandTotal float64
	groupedMap := make(map[string]*CategoryGroup)

	for _, p := range products {
		var dailyLedgers []models.StockLedger

		// 4. Find all ledger entries for this product within our specific time window where stock went UP (+)
		database.DB.Where(
			"product_id = ? AND created_at >= ? AND created_at <= ? AND change_amount > 0",
			p.ID, startOfDay, endOfDay,
		).Find(&dailyLedgers)

		totalAddedToday := 0
		for _, ledger := range dailyLedgers {
			totalAddedToday += ledger.ChangeAmount
		}

		if totalAddedToday <= 0 {
			continue
		}

		// --- Grouping and Math ---
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

		itemTotal := float64(totalAddedToday) * p.CostPrice

		valItem := ValuationItem{
			Name:      p.Name,
			Quantity:  totalAddedToday,
			CostPrice: p.CostPrice,
			TotalCost: itemTotal,
		}

		groupedMap[catName].Items = append(groupedMap[catName].Items, valItem)
		groupedMap[catName].Subtotal += itemTotal
		grandTotal += itemTotal
	}

	var response ValuationResponse
	response.GrandTotal = grandTotal
	for _, group := range groupedMap {
		response.Categories = append(response.Categories, *group)
	}

	c.JSON(http.StatusOK, response)
}
