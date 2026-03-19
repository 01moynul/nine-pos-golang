package handlers

import (
	"fmt"
	"net/http"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
)

// CreateExpense logs a new operational cost and handles Till Payouts
func CreateExpense(c *gin.Context) {
	var input struct {
		ExpenseType string  `json:"expense_type"`
		Amount      float64 `json:"amount"`
		Date        string  `json:"date"` // Expecting YYYY-MM-DD
		Description string  `json:"description"`

		// --- NEW: Security & Till Fields ---
		PaidFromTill     bool   `json:"paid_from_till"`
		ShiftID          *uint  `json:"shift_id"`
		SecurityVideoURL string `json:"security_video_url"`
	}

	// 1. Read the incoming JSON data from the frontend
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 2. Parse the date in the LOCAL timezone so it matches the dashboard filters
	parsedDate, err := time.ParseInLocation("2006-01-02", input.Date, time.Now().Location())
	if err != nil {
		parsedDate = time.Now() // Fallback to today if parsing fails
	}

	// 3. Safely get the user identifier from the JWT token (set by middleware)
	var loggedBy string
	if userID, exists := c.Get("userID"); exists {
		loggedBy = fmt.Sprintf("User ID: %v", userID)
	} else {
		loggedBy = "System"
	}

	// 4. Build the Expense object, mapping all the new Till Payout data
	expense := models.Expense{
		ExpenseType:      input.ExpenseType,
		Amount:           input.Amount,
		Date:             parsedDate,
		Description:      input.Description,
		LoggedBy:         loggedBy,
		PaidFromTill:     input.PaidFromTill,
		ShiftID:          input.ShiftID,
		SecurityVideoURL: input.SecurityVideoURL,
		CreatedAt:        time.Now(),
	}

	// 5. Save the expense to the database
	if err := database.DB.Create(&expense).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log expense"})
		return
	}

	// 6. Return success.
	// NOTE: By only returning a 201 Created AFTER a successful DB save,
	// we guarantee the frontend will only kick the physical drawer open
	// if the math is safely secured in our ledger.
	c.JSON(http.StatusCreated, gin.H{
		"message": "Expense logged successfully",
		"expense": expense,
	})
}

// ExpenseReport defines the shape of our new P&L response
type ExpenseReport struct {
	Expenses       []models.Expense `json:"expenses"`
	TotalExpenses  float64          `json:"total_expenses"`
	GrossProfit    float64          `json:"gross_profit"`
	StandingProfit float64          `json:"standing_profit"`
}

// GetExpenses retrieves expenses and calculates Standing Profit based on Date Filters
func GetExpenses(c *gin.Context) {
	var data ExpenseReport

	// 1. Time Filter Logic (Matches Sales Report)
	timeframe := c.Query("timeframe")
	customStart := c.Query("customStart")
	customEnd := c.Query("customEnd")

	now := time.Now()
	var startTime, endTime time.Time

	if timeframe == "custom" && customStart != "" && customEnd != "" {
		// Use ParseInLocation to respect the local timezone
		startTime, _ = time.ParseInLocation("2006-01-02T15:04", customStart, now.Location())
		endTime, _ = time.ParseInLocation("2006-01-02T15:04", customEnd, now.Location())
	} else {
		switch timeframe {
		case "today":
			startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		case "7days":
			startTime = now.AddDate(0, 0, -7)
		case "30days":
			startTime = now.AddDate(0, 0, -30)
		}
	}

	// 2. Fetch Expenses for Timeframe
	expensesQuery := database.DB.Order("date desc")
	if !startTime.IsZero() {
		expensesQuery = expensesQuery.Where("date >= ?", startTime)
	}
	if !endTime.IsZero() {
		expensesQuery = expensesQuery.Where("date <= ?", endTime)
	}

	if err := expensesQuery.Find(&data.Expenses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch expenses"})
		return
	}

	// Calculate Total Expenses
	for _, e := range data.Expenses {
		data.TotalExpenses += e.Amount
	}

	// 3. Fetch Gross Profit for the EXACT SAME Timeframe
	salesQuery := database.DB.Table("sale_items").
		Joins("JOIN sales ON sale_items.sale_id = sales.id").
		Where("sales.status = ?", "completed")

	if !startTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time >= ?", startTime)
	}
	if !endTime.IsZero() {
		salesQuery = salesQuery.Where("sales.sale_time <= ?", endTime)
	}

	// Gross Profit = SUM(Quantity * (SellPrice - BuyPrice))
	row := salesQuery.Select("COALESCE(SUM(sale_items.quantity * (sale_items.price_at_sale - sale_items.buy_price_rm)), 0)").Row()
	row.Scan(&data.GrossProfit)

	// 4. Calculate Current Standing Profit
	data.StandingProfit = data.GrossProfit - data.TotalExpenses

	c.JSON(http.StatusOK, data)
}

// DeleteExpense removes an accidentally logged expense
func DeleteExpense(c *gin.Context) {
	id := c.Param("id")

	if err := database.DB.Delete(&models.Expense{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete expense"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Expense deleted successfully"})
}
