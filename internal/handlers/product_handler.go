package handlers

import (
	"bytes"        // NEW
	"encoding/csv" // NEW
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"
	"go-pos-agent/internal/services"
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

	// --- UPGRADED: Ledger Preparation (Fractional Weights) ---
	oldStock := product.StockQuantity
	var newStock float64 // MUST BE float64
	stockChanged := false

	// Safely check if the incoming JSON contains a change to "stock_quantity"
	if sq, exists := updateData["stock_quantity"]; exists {
		if val, ok := sq.(float64); ok {
			newStock = val // Directly use the float64
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
		ProductID int     `json:"product_id"`
		Quantity  float64 `json:"quantity"` // UPGRADED: Float64 for weights
	} `json:"items"`
	RequestEInvoice bool `json:"request_einvoice"`
}

func ProcessSale(c *gin.Context) {
	var req SaleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// Get User ID from the Context (set by Middleware)
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

		// --- Ledger Interceptor ---
		ledgerEntry := models.StockLedger{
			ProductID:    product.ID,
			ChangeAmount: -item.Quantity,
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
		totalAmount += product.Price * item.Quantity

		// Prepare Sale Item record
		saleItems = append(saleItems, models.SaleItem{
			ProductID:   product.ID,
			Quantity:    item.Quantity,
			BuyPriceRM:  product.CostPrice,
			PriceAtSale: product.Price,
		})
	}

	// Generate a Unique Receipt ID
	uniqueReceiptID := fmt.Sprintf("RCPT-%d", time.Now().Unix())

	// 3. Create the Sale Header
	sale := models.Sale{
		ReceiptID:   uniqueReceiptID,
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

	// ==========================================
	// --- NEW: LHDN Sandbox Integration ---
	// ==========================================

	// Check if the frontend passed a flag requesting an official e-Invoice.
	customerRequestedEInvoice := req.RequestEInvoice

	var lhdnData services.LHDNResponse
	if customerRequestedEInvoice {
		// Ping our isolated LHDN Sandbox Engine.
		// We pass the finalized 'sale' object so the engine knows what to process.
		lhdnData = services.SubmitToLHDNSandbox(sale)
	}
	// ==========================================

	// 5. Final Response Payload
	c.JSON(http.StatusOK, gin.H{
		"message": "Sale successful!",
		"sale_id": sale.ID,
		"total":   totalAmount,
		"lhdn":    lhdnData, // <-- NEW: Passes the Mock QR URL and Validation ID back to React
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

// --- GET: /api/products/scale-export ---
// Generates a .csv file compatible with Rongta / Link64 scale software
func ExportWeighableProducts(c *gin.Context) {
	var products []models.Product

	// 1. Only fetch items explicitly marked as weighable
	if err := database.DB.Where("is_weighable = ?", true).Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch weighable products"})
		return
	}

	// 2. Create an in-memory CSV writer
	b := &bytes.Buffer{}
	writer := csv.NewWriter(b)

	// 3. Write the exact Column Headers expected by the Scale Software
	writer.Write([]string{"PLU_ID", "Item_Code", "Name", "Price", "UnitType", "BarcodeType"})

	// 4. Loop through the products and write the rows
	for _, p := range products {
		pluID := fmt.Sprintf("%d", p.ID)
		itemCode := p.SKU
		if itemCode == "" {
			itemCode = pluID // Fallback if no SKU exists
		}

		name := p.Name
		price := fmt.Sprintf("%.2f", p.Price) // Ensure exactly 2 decimal places
		unitType := "kg"                      // Base unit
		barcodeType := "13"                   // EAN-13 format for the printed sticker

		writer.Write([]string{pluID, itemCode, name, price, unitType, barcodeType})
	}

	// Ensure all data is pushed to the buffer
	writer.Flush()

	// 5. Force the browser to download this as a file instead of displaying it as text
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", "attachment; filename=rongta_plu_export.csv")
	c.Header("Content-Type", "text/csv")

	c.String(http.StatusOK, b.String())
}
