package handlers

import (
	"fmt"
	"net/http"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
)

// CreateExpense logs a new operational cost
func CreateExpense(c *gin.Context) {
	var input struct {
		ExpenseType string  `json:"expense_type"`
		Amount      float64 `json:"amount"`
		Date        string  `json:"date"` // Expecting YYYY-MM-DD
		Description string  `json:"description"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Parse the date
	parsedDate, err := time.Parse("2006-01-02", input.Date)
	if err != nil {
		parsedDate = time.Now() // Fallback to today if parsing fails
	}

	// Safely get the user identifier from the JWT token (set by middleware)
	var loggedBy string
	if userID, exists := c.Get("userID"); exists {
		// Use fmt to safely convert whatever number format userID is into a string
		loggedBy = fmt.Sprintf("User ID: %v", userID)
	} else {
		loggedBy = "Admin" // Safe fallback
	}

	expense := models.Expense{
		ExpenseType: input.ExpenseType,
		Amount:      input.Amount,
		Date:        parsedDate,
		Description: input.Description,
		LoggedBy:    loggedBy,
		CreatedAt:   time.Now(),
	}

	if err := database.DB.Create(&expense).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log expense"})
		return
	}

	c.JSON(http.StatusCreated, expense)
}

// GetExpenses retrieves expenses, optionally filtered by month
func GetExpenses(c *gin.Context) {
	var expenses []models.Expense

	// Basic implementation: fetch all for now, ordered by newest first
	// (We can add month/year query parameters later if needed for the UI)
	if err := database.DB.Order("date desc").Find(&expenses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch expenses"})
		return
	}

	c.JSON(http.StatusOK, expenses)
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
