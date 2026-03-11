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
		Sold        float64 `json:"sold"`
		Revenue     float64 `json:"revenue"`
		Profit      float64 `json:"profit"`
	} `json:"top_selling"`
	RecentSales []models.Sale              `json:"recent_sales"`
	VoidedSales []models.VoidedTransaction `json:"voided_sales"` // NEW: Task 2.4 Security Audits
}

// --- GET: /api/reports ---
func GetSalesReport(c *gin.Context) {
	var data ReportData

	// --- 1. DYNAMIC TIME & SEARCH FILTER LOGIC ---
	timeframe := c.Query("timeframe")     // e.g., "today", "7days", "30days", "custom"
	customStart := c.Query("customStart") // e.g., "2026-03-07T11:00"
	customEnd := c.Query("customEnd")     // e.g., "2026-03-07T17:00"
	searchQuery := c.Query("search")      // NEW: Item-specific filter
	categoryFilter := c.Query("category") // --- NEW: Category filter ---

	now := time.Now()
	var startTime time.Time
	var endTime time.Time

	if timeframe == "custom" && customStart != "" && customEnd != "" {
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
			startTime = time.Time{}
		}
	}

	// --- 2. REVENUE & PROFIT (Filtered) ---
	salesQuery := database.DB.Table("sale_items").
		Joins("JOIN sales ON sale_items.sale_id = sales.id").
		Where("sales.status = ?", "completed")

	// --- UPGRADED: Inject Product Search & Category Filters ---
	if searchQuery != "" || (categoryFilter != "" && categoryFilter != "All") {
		salesQuery = salesQuery.Joins("JOIN products ON sale_items.product_id = products.id")
	}
	if searchQuery != "" {
		salesQuery = salesQuery.Where("products.name LIKE ? OR products.sku LIKE ?", "%"+searchQuery+"%", "%"+searchQuery+"%")
	}
	if categoryFilter != "" && categoryFilter != "All" {
		salesQuery = salesQuery.Where("products.category = ?", categoryFilter)
	}

	if !startTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time <= ?", endTime)
	}

	row := salesQuery.
		Select("COALESCE(SUM(sale_items.quantity * sale_items.price_at_sale), 0), COALESCE(SUM(sale_items.quantity * (sale_items.price_at_sale - sale_items.buy_price_rm)), 0)").
		Row()

	if err := row.Scan(&data.TotalRevenue, &data.TotalProfit); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to calculate revenue and profit"})
		return
	}

	// --- 3. TOTAL ORDERS (Filtered) ---
	ordersQuery := database.DB.Model(&models.Sale{}).Where("status = ?", "completed")

	// --- UPGRADED: Inject Product Search & Category Filters (Subquery) ---
	if searchQuery != "" || (categoryFilter != "" && categoryFilter != "All") {
		subQuery := database.DB.Table("sale_items").Select("sale_items.sale_id").Joins("JOIN products ON sale_items.product_id = products.id")
		if searchQuery != "" {
			subQuery = subQuery.Where("products.name LIKE ? OR products.sku LIKE ?", "%"+searchQuery+"%", "%"+searchQuery+"%")
		}
		if categoryFilter != "" && categoryFilter != "All" {
			subQuery = subQuery.Where("products.category = ?", categoryFilter)
		}
		ordersQuery = ordersQuery.Where("id IN (?)", subQuery)
	}

	if !startTime.IsZero() {
		ordersQuery = ordersQuery.Where("sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		ordersQuery = ordersQuery.Where("sale_time <= ?", endTime)
	}

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

	// --- UPGRADED: Inject Product Search & Category Filters ---
	if searchQuery != "" {
		topSellingQuery = topSellingQuery.Where("products.name LIKE ? OR products.sku LIKE ?", "%"+searchQuery+"%", "%"+searchQuery+"%")
	}
	if categoryFilter != "" && categoryFilter != "All" {
		topSellingQuery = topSellingQuery.Where("products.category = ?", categoryFilter)
	}

	if !startTime.IsZero() {
		topSellingQuery = topSellingQuery.Where("sales.sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		topSellingQuery = topSellingQuery.Where("sales.sale_time <= ?", endTime)
	}

	err := topSellingQuery.Group("products.name").Order("sold desc").Limit(5).Scan(&data.TopSelling).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch top selling items"})
		return
	}

	// --- 5. RECENT SALES & VOIDED (Filtered) ---
	recentSalesQuery := database.DB.Preload("Items").Preload("Items.Product").Order("sale_time desc").Limit(10)

	// --- UPGRADED: Inject Product Search & Category Filters (Subquery) ---
	if searchQuery != "" || (categoryFilter != "" && categoryFilter != "All") {
		subQuery := database.DB.Table("sale_items").Select("sale_items.sale_id").Joins("JOIN products ON sale_items.product_id = products.id")
		if searchQuery != "" {
			subQuery = subQuery.Where("products.name LIKE ? OR products.sku LIKE ?", "%"+searchQuery+"%", "%"+searchQuery+"%")
		}
		if categoryFilter != "" && categoryFilter != "All" {
			subQuery = subQuery.Where("products.category = ?", categoryFilter)
		}
		recentSalesQuery = recentSalesQuery.Where("id IN (?)", subQuery)
	}

	if !startTime.IsZero() {
		recentSalesQuery = recentSalesQuery.Where("sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		recentSalesQuery = recentSalesQuery.Where("sale_time <= ?", endTime)
	}
	recentSalesQuery.Find(&data.RecentSales)

	voidedQuery := database.DB.Order("timestamp desc").Limit(10)
	if !startTime.IsZero() {
		voidedQuery = voidedQuery.Where("timestamp >= ?", startTime)
	}
	if !endTime.IsZero() {
		voidedQuery = voidedQuery.Where("timestamp <= ?", endTime)
	}
	voidedQuery.Find(&data.VoidedSales)

	// Send data to frontend
	c.JSON(http.StatusOK, data)
}

// --- DATA STRUCTURES FOR VALUATION REPORT ---

// ValuationItem represents a single row in the PDF table
type ValuationItem struct {
	Name          string  `json:"name"`
	Quantity      float64 `json:"quantity"` // UPGRADED: Changed from int to float64
	CostPrice     float64 `json:"cost_price"`
	TotalCost     float64 `json:"total_cost"`
	SellPrice     float64 `json:"sell_price"`      // NEW: For Profitability View
	ProfitPerUnit float64 `json:"profit_per_unit"` // NEW: For Profitability View
	TotalProfit   float64 `json:"total_profit"`    // NEW: For Profitability View
}

// CategoryGroup represents one entire table in the PDF (e.g., "DRINKS")
type CategoryGroup struct {
	CategoryName   string          `json:"category_name"`
	Items          []ValuationItem `json:"items"`
	Subtotal       float64         `json:"subtotal"`
	ProfitSubtotal float64         `json:"profit_subtotal"` // NEW: For Profitability View
}

// ValuationResponse is the final payload sent to React
type ValuationResponse struct {
	Categories       []CategoryGroup `json:"categories"`
	GrandTotal       float64         `json:"grand_total"`
	GrandTotalProfit float64         `json:"grand_total_profit"` // NEW: For Profitability View
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
	var grandTotalProfit float64 // NEW
	groupedMap := make(map[string]*CategoryGroup)

	// 3. Loop through every single product in the database
	for _, p := range products {
		catName := p.Category
		if catName == "" {
			catName = "Uncategorized"
		}

		if _, exists := groupedMap[catName]; !exists {
			groupedMap[catName] = &CategoryGroup{
				CategoryName:   catName,
				Items:          []ValuationItem{},
				Subtotal:       0,
				ProfitSubtotal: 0, // NEW
			}
		}

		itemTotal := p.StockQuantity * p.CostPrice     // UPGRADED
		profitPerUnit := p.Price - p.CostPrice         // NEW
		totalProfit := p.StockQuantity * profitPerUnit // UPGRADED

		valItem := ValuationItem{
			Name:          p.Name,
			Quantity:      p.StockQuantity,
			CostPrice:     p.CostPrice,
			TotalCost:     itemTotal,
			SellPrice:     p.Price,       // NEW
			ProfitPerUnit: profitPerUnit, // NEW
			TotalProfit:   totalProfit,   // NEW
		}

		groupedMap[catName].Items = append(groupedMap[catName].Items, valItem)
		groupedMap[catName].Subtotal += itemTotal
		groupedMap[catName].ProfitSubtotal += totalProfit // NEW
		grandTotal += itemTotal
		grandTotalProfit += totalProfit // NEW
	}

	// 4. Convert the Map into a flat Array (Slice) so React can easily loop over it
	var response ValuationResponse
	response.GrandTotal = grandTotal
	response.GrandTotalProfit = grandTotalProfit // NEW
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
	var grandTotalProfit float64 // ADD THIS LINE HERE
	groupedMap := make(map[string]*CategoryGroup)

	for _, p := range products {
		var dailyLedgers []models.StockLedger

		// 4. Find all ledger entries for this product within our specific time window where stock went UP (+)
		database.DB.Where(
			"product_id = ? AND created_at >= ? AND created_at <= ? AND change_amount > 0",
			p.ID, startOfDay, endOfDay,
		).Find(&dailyLedgers)

		var totalAddedToday float64 // UPGRADED: Now a decimal
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
				CategoryName:   catName,
				Items:          []ValuationItem{},
				Subtotal:       0,
				ProfitSubtotal: 0, // NEW
			}
		}

		itemTotal := totalAddedToday * p.CostPrice     // UPGRADED
		profitPerUnit := p.Price - p.CostPrice         // NEW
		totalProfit := totalAddedToday * profitPerUnit // UPGRADED

		valItem := ValuationItem{
			Name:          p.Name,
			Quantity:      totalAddedToday,
			CostPrice:     p.CostPrice,
			TotalCost:     itemTotal,
			SellPrice:     p.Price,       // NEW
			ProfitPerUnit: profitPerUnit, // NEW
			TotalProfit:   totalProfit,   // NEW
		}

		groupedMap[catName].Items = append(groupedMap[catName].Items, valItem)
		groupedMap[catName].Subtotal += itemTotal
		groupedMap[catName].ProfitSubtotal += totalProfit // NEW
		grandTotal += itemTotal
		grandTotalProfit += totalProfit // NEW
	}

	var response ValuationResponse
	response.GrandTotal = grandTotal
	response.GrandTotalProfit = grandTotalProfit // NEW
	for _, group := range groupedMap {
		response.Categories = append(response.Categories, *group)
	}

	c.JSON(http.StatusOK, response)
}
