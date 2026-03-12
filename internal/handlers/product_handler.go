package handlers

import (
	"bytes"
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
	"github.com/xuri/excelize/v2"
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
// Generates a native .xlsx file compatible with advanced scale software
func ExportWeighableProducts(c *gin.Context) {
	var products []models.Product

	// 1. Only fetch items explicitly marked as weighable
	if err := database.DB.Where("is_weighable = ?", true).Find(&products).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch weighable products"})
		return
	}

	// 2. Initialize a new Excel File
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Println("Error closing excel file:", err)
		}
	}()

	// 3. Write the exact Column Headers expected by the Rongta Scale Software (Row 1)
	// We include all 24 columns to guarantee perfect copy-paste alignment.
	headers := []string{
		"Hotkey", "Name", "LFCode", "Code", "Barcode Type", "Unit Price",
		"Unit Weight", "Unit Amount", "Department", "PT Weight", "Shelf Time",
		"Pack Type", "Tare", "Error(%)", "Message1", "Message2", "Label",
		"Discount/Table", "Account", "sPluFieldTitle20", "Account",
		"Recommend days", "nutrition", "Ice(%)",
	}

	for i, header := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue("Sheet1", cell, header)
	}

	// 4. Loop through the products and write the rows (Starting at Row 2)
	for i, p := range products {
		row := i + 2 // i starts at 0, headers are on row 1

		itemCode := p.SKU
		if itemCode == "" {
			itemCode = fmt.Sprintf("%d", p.ID) // Fallback if no SKU exists
		}

		// --- SMART HOTKEY LOGIC ---
		// Convert the 5-digit SKU (e.g., "00007") into a simple scale key (7).
		pluInt, err := strconv.Atoi(itemCode)
		var hotkey string
		if err == nil && pluInt > 0 {
			hotkey = fmt.Sprintf("%d", pluInt)
		} else {
			hotkey = fmt.Sprintf("%d", i+1)
		}

		// Fill out the 24 columns with your real data and safe Rongta defaults
		f.SetCellValue("Sheet1", fmt.Sprintf("A%d", row), hotkey)   // Hotkey
		f.SetCellValue("Sheet1", fmt.Sprintf("B%d", row), p.Name)   // Name
		f.SetCellValue("Sheet1", fmt.Sprintf("C%d", row), hotkey)   // LFCode
		f.SetCellValue("Sheet1", fmt.Sprintf("D%d", row), itemCode) // Code (SKU)
		f.SetCellValue("Sheet1", fmt.Sprintf("E%d", row), 13)       // Barcode Type
		f.SetCellValue("Sheet1", fmt.Sprintf("F%d", row), p.Price)  // Unit Price
		f.SetCellValue("Sheet1", fmt.Sprintf("G%d", row), "Kg")     // Unit Weight
		f.SetCellValue("Sheet1", fmt.Sprintf("H%d", row), 0)        // Unit Amount
		f.SetCellValue("Sheet1", fmt.Sprintf("I%d", row), 21)       // Department
		f.SetCellValue("Sheet1", fmt.Sprintf("J%d", row), "0.000")  // PT Weight
		f.SetCellValue("Sheet1", fmt.Sprintf("K%d", row), 15)       // Shelf Time
		f.SetCellValue("Sheet1", fmt.Sprintf("L%d", row), "Normal") // Pack Type
		f.SetCellValue("Sheet1", fmt.Sprintf("M%d", row), "0.000")  // Tare
		f.SetCellValue("Sheet1", fmt.Sprintf("N%d", row), 0)        // Error(%)
		f.SetCellValue("Sheet1", fmt.Sprintf("O%d", row), 0)        // Message1
		f.SetCellValue("Sheet1", fmt.Sprintf("P%d", row), 0)        // Message2
		f.SetCellValue("Sheet1", fmt.Sprintf("Q%d", row), 0)        // Label
		f.SetCellValue("Sheet1", fmt.Sprintf("R%d", row), 0)        // Discount/Table
		f.SetCellValue("Sheet1", fmt.Sprintf("S%d", row), 0)        // Account
		f.SetCellValue("Sheet1", fmt.Sprintf("T%d", row), "")       // sPluFieldTitle20
		f.SetCellValue("Sheet1", fmt.Sprintf("U%d", row), 0)        // Account
		f.SetCellValue("Sheet1", fmt.Sprintf("V%d", row), 0)        // Recommend days
		f.SetCellValue("Sheet1", fmt.Sprintf("W%d", row), 0)        // nutrition
		f.SetCellValue("Sheet1", fmt.Sprintf("X%d", row), 0)        // Ice(%)
	}

	// 5. Write the Excel file to a memory buffer
	var b bytes.Buffer
	if err := f.Write(&b); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate Excel file"})
		return
	}

	// 6. Force the browser to download this as a native Excel file (.xlsx)
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", "attachment; filename=rongta_plu_export.xlsx")
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")

	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", b.Bytes())
}
