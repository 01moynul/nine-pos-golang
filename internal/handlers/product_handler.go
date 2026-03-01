package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"
	"go-pos-agent/internal/utils"

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

	// --- NEW: Ledger Interceptor ---
	// If the user created the product with initial stock, log it in the ledger!
	if newProduct.StockQuantity > 0 {
		ledgerEntry := models.StockLedger{
			ProductID:    newProduct.ID,
			ChangeAmount: newProduct.StockQuantity,
			Balance:      newProduct.StockQuantity,
			Reason:       "Initial Setup",
			CreatedAt:    time.Now(),
		}
		database.DB.Create(&ledgerEntry) // Silently save in background
	}
	// -------------------------------

	c.JSON(http.StatusCreated, newProduct)
}

// --- PUT: Update Price or Stock ---
func UpdateProduct(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Product ID"})
		return
	}

	var product models.Product
	if err := database.DB.First(&product, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Product not found"})
		return
	}

	var updateData map[string]interface{}
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// --- NEW: Ledger Preparation ---
	// Remember the old stock before we overwrite it
	oldStock := product.StockQuantity
	var newStock int
	stockChanged := false

	// Safely check if the incoming JSON contains a change to "stock_quantity"
	if sq, exists := updateData["stock_quantity"]; exists {
		// JSON numbers default to float64, so we must safely convert to int
		if val, ok := sq.(float64); ok {
			newStock = int(val)
			stockChanged = true
		}
	}
	// -------------------------------

	// 4. Save updates to the Product
	if err := database.DB.Model(&product).Updates(updateData).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update product"})
		return
	}

	// --- NEW: Ledger Interceptor ---
	// If the stock was changed, calculate the difference and record it
	if stockChanged && oldStock != newStock {
		changeAmount := newStock - oldStock // e.g., 50 - 40 = +10
		ledgerEntry := models.StockLedger{
			ProductID:    product.ID,
			ChangeAmount: changeAmount,
			Balance:      newStock,
			Reason:       "Manual Audit / Restock",
			CreatedAt:    time.Now(),
		}
		database.DB.Create(&ledgerEntry)
	}
	// -------------------------------

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

		// --- NEW: Ledger Interceptor ---
		// We write this inside the 'tx' transaction. If the sale fails, this ledger entry is erased!
		ledgerEntry := models.StockLedger{
			ProductID:    product.ID,
			ChangeAmount: -item.Quantity, // Negative because it's leaving the store
			Balance:      product.StockQuantity,
			Reason:       "Sale Checkout",
			CreatedAt:    time.Now(),
		}
		if err := tx.Create(&ledgerEntry).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write audit ledger"})
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

	// --- NEW FIX: Generate a Unique Receipt ID (Roadmap Task 2.3) ---
	// Example Output: RCPT-1709123456
	uniqueReceiptID := fmt.Sprintf("RCPT-%d", time.Now().Unix())

	// 3. Create the Sale Header
	sale := models.Sale{
		ReceiptID:   uniqueReceiptID, // <--- THE FIX: Assign the generated ID
		UserID:      userID,
		TotalAmount: totalAmount,
		SaleTime:    time.Now(),
		Status:      "completed",
		Items:       saleItems,
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

// --- GET: Scan a barcode (Standard or Smart Scale) ---
// This handles Rapid Hardware Integration (Task 1.2)
func ScanProduct(c *gin.Context) {
	// 1. Grab the scanned barcode string from the URL parameter
	barcode := c.Param("barcode")

	// 2. Pass the barcode through our new parsing engine
	scaleData := utils.ParseEAN13(barcode)

	var product models.Product

	if scaleData.IsScaleBarcode {
		// 3. SCALE ITEM DETECTED: Lookup the base product using the 5-digit ItemID.
		// We assume your Deli/Meat base products are saved with their 5-digit SKU.
		if err := database.DB.Where("sku = ?", scaleData.ItemID).First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Base scale product not found", "sku": scaleData.ItemID})
			return
		}

		// 4. DYNAMIC OVERRIDE: Replace the database base price with the exact calculated price
		// embedded in the physical barcode sticker so the cart charges the right amount.
		product.Price = scaleData.CalculatedPrice

	} else {
		// 5. STANDARD ITEM DETECTED: Do a direct 1-to-1 lookup for the full 13 digits.
		if err := database.DB.Where("sku = ?", barcode).First(&product).Error; err != nil {
			// Returning a 404 is crucial here!
			// In Task 1.3, the React frontend will use this exact 404 to auto-trigger the "Add Product" modal.
			c.JSON(http.StatusNotFound, gin.H{"error": "Product not found", "sku": barcode})
			return
		}
	}

	// 6. Return the perfectly formatted product back to the frontend
	c.JSON(http.StatusOK, product)
}
