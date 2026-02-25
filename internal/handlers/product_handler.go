package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// --- GET: List all products ---
// This tool allows the AI to "See" what is in the shop.
func GetProducts(c *gin.Context) {
	var products []models.Product

	// Fetch from DB
	result := database.DB.Find(&products)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch products"})
		return
	}

	c.JSON(http.StatusOK, products)
}

// --- POST: Add a new product ---
func AddProduct(c *gin.Context) {
	var newProduct models.Product

	// 1. Parse JSON Input
	if err := c.ShouldBindJSON(&newProduct); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 2. Save to DB
	if err := database.DB.Create(&newProduct).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create product"})
		return
	}

	c.JSON(http.StatusCreated, newProduct)
}

// --- PUT: Update Price or Stock ---
// The AI will use this tool to change prices.
func UpdateProduct(c *gin.Context) {
	// 1. Get ID from URL (e.g., /products/5)
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Product ID"})
		return
	}

	// 2. Find existing product
	var product models.Product
	if err := database.DB.First(&product, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	// 3. Update fields based on JSON input
	// We use a map so we only update what was sent (partial update)
	var updateData map[string]interface{}
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 4. Save updates
	if err := database.DB.Model(&product).Updates(updateData).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Product updated successfully", "product": product})
}

// --- ADD THIS AT THE BOTTOM ---

// SaleRequest defines what the Frontend sends us
type SaleRequest struct {
	Items []struct {
		ProductID int `json:"product_id"`
		Quantity  int `json:"quantity"`
	} `json:"items"`
}

func ProcessSale(c *gin.Context) {
	var req SaleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Get User ID from the Context (set by Middleware)
	// (In a real app, use this to track WHO sold it)
	userID := c.MustGet("userID").(uint)

	// 1. Start a Database Transaction (ACID Safety)
	tx := database.DB.Begin()

	var totalAmount float64
	var saleItems []models.SaleItem

	// 2. Loop through cart items
	for _, item := range req.Items {
		var product models.Product

		// Lock the row to prevent race conditions
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&product, item.ProductID).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Product %d not found", item.ProductID)})
			return
		}

		// Check Stock
		if product.StockQuantity < item.Quantity {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Insufficient stock for %s", product.Name)})
			return
		}

		// Deduct Stock
		product.StockQuantity -= item.Quantity
		if err := tx.Save(&product).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update stock"})
			return
		}

		// Calculate Price for this item
		totalAmount += product.Price * float64(item.Quantity)

		// Prepare Sale Item record
		saleItems = append(saleItems, models.SaleItem{
			ProductID:   product.ID,
			Quantity:    item.Quantity,
			PriceAtSale: product.Price,
		})
	}

	// 3. Create the Sale Header
	sale := models.Sale{
		UserID:      userID,
		TotalAmount: totalAmount,
		SaleTime:    time.Now(),
		Status:      "completed",
		Items:       saleItems, // GORM will automatically insert these!
	}

	if err := tx.Create(&sale).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sale record"})
		return
	}

	// 4. Commit Transaction
	tx.Commit()

	c.JSON(http.StatusOK, gin.H{
		"message": "Sale successful!",
		"sale_id": sale.ID,
		"total":   totalAmount,
	})
}

// --- DELETE: Remove a product ---
func DeleteProduct(c *gin.Context) {
	id := c.Param("id")

	// Attempt to delete
	// Unscoped() is optional: use it if you want to PERMANENTLY remove it.
	// Without Unscoped(), GORM will just set a "deleted_at" timestamp (Soft Delete) if your model supports it.
	if err := database.DB.Delete(&models.Product{}, id).Error; err != nil {
		// This usually happens if the product is linked to existing sales (Foreign Key Constraint)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Could not delete product. It might be linked to past sales."})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Product deleted successfully"})
}

// --- UPLOAD: Handle Image Files ---
func UploadImage(c *gin.Context) {
	// 1. Get the file from the request
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// 2. Security Check (Only allow images)
	// (Simple extension check - for production, check MIME types)
	// ...

	// 3. Generate a safe unique filename
	// e.g., "167890123_burger.jpg"
	filename := fmt.Sprintf("%d_%s", time.Now().Unix(), file.Filename)
	filepath := "./uploads/" + filename

	// 4. Save the file to the 'uploads' folder
	if err := c.SaveUploadedFile(file, filepath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
		return
	}

	// Get the Base URL from .env (e.g., http://localhost:8080 or https://your-site.com)
	baseURL := os.Getenv("BASE_URL")

	// Safety check: if .env is missing, default to localhost
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}

	fullURL := baseURL + "/uploads/" + filename
	c.JSON(http.StatusOK, gin.H{
		"message": "File uploaded successfully",
		"url":     fullURL,
	})
}
