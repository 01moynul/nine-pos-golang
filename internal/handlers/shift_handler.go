package handlers

import (
	"log"
	"net/http"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
)

// ==========================================
// 1. SETTINGS & STATUS CHECKS
// ==========================================

// GetStoreSettings fetches the master config (Admin Kill Switch for Shifts)
func GetStoreSettings(c *gin.Context) {
	var settings models.StoreSettings
	if err := database.DB.First(&settings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch store settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// GetActiveShift checks if there is currently an open register session
func GetActiveShift(c *gin.Context) {
	var activeShift models.ShiftLog

	// Look for a shift where Status is "open" and ClosedAt is null
	err := database.DB.Where("status = ?", "open").Where("closed_at IS NULL").First(&activeShift).Error

	if err != nil {
		// 404 intentionally triggers the Frontend's "Register Closed" lockout UI
		c.JSON(http.StatusNotFound, gin.H{"message": "No active shift found. Register is closed."})
		return
	}

	c.JSON(http.StatusOK, activeShift)
}

// ==========================================
// 2. SHIFT OPENING & SECURITY CAMERA (SCENARIO B)
// ==========================================

// UnlockRegister represents the cashier clicking "Unlock" to begin counting
func UnlockRegister(c *gin.Context) {
	// Note: We instruct the React Frontend to call your existing POST /printer/kick-drawer
	// at the exact same time it calls this endpoint to pop the physical tray.

	// Generate a unique ID for this morning float video
	sessionID := "shift_open_" + time.Now().Format("20060102_150405")

	// The frontend takes this SessionID and instantly calls POST /security/start to turn on the camera
	c.JSON(http.StatusOK, gin.H{
		"message":    "Register unlocked for counting.",
		"session_id": sessionID,
	})
}

// OpenShiftRequest expects the counted cash and the camera session ID
type OpenShiftRequest struct {
	OpeningCash      float64 `json:"opening_cash"`
	SessionID        string  `json:"session_id"`
	OpeningBreakdown string  `json:"opening_breakdown"` // <-- NEW
}

// OpenShift saves the float and triggers the 15-second camera overhang to watch the drawer close
func OpenShift(c *gin.Context) {
	var req OpenShiftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// 1. Double-check no shift is already active
	var count int64
	database.DB.Model(&models.ShiftLog{}).Where("status = ?", "open").Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "A shift is already open."})
		return
	}

	// 2. Get cashier's ID from token and lookup Username
	userID, exists := c.Get("userID")
	openedBy := "Unknown Staff"

	if exists {
		var user models.User
		// Quickly ask the database for the username attached to this ID
		if err := database.DB.Select("username").First(&user, userID).Error; err == nil {
			openedBy = user.Username
		}
	}

	// 3. Create the ShiftLog record
	newShift := models.ShiftLog{
		OpenedAt:         time.Now(),
		OpenedBy:         openedBy,
		OpeningCash:      req.OpeningCash,
		OpeningBreakdown: req.OpeningBreakdown, // <-- NEW: Save to DB
		Status:           "open",
		OpeningVideoURL:  "PENDING_UPLOAD_" + req.SessionID,
	}

	if err := database.DB.Create(&newShift).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open shift"})
		return
	}

	// 4. 🔥 Launch the 15-second overhang in the background so the POS unlocks immediately!
	go finalizeShiftVideo(req.SessionID, newShift.ID)

	c.JSON(http.StatusOK, gin.H{
		"message": "Shift confirmed and opened successfully",
		"shift":   newShift,
	})
}

// finalizeShiftVideo waits 15s (or cuts short if a sale starts), stops the camera, and saves the link
func finalizeShiftVideo(sessionID string, shiftID uint) {
	// Securely access the camera memory map from security_handler.go
	recordingMutex.Lock()
	rec, exists := activeRecordings[sessionID]
	if exists {
		rec.IsClosing = true // Flag it so we know it's in overhang mode
	}
	recordingMutex.Unlock()

	if !exists {
		log.Println("⚠️ SECURITY: No active recording found for shift session:", sessionID)
		return
	}

	log.Printf("🎥 SECURITY: Shift %d confirmed. Starting 15s overhang for drawer closing...", shiftID)

	// Wait exactly 15 seconds, OR stop immediately if the frontend triggers a new cart session
	select {
	case <-time.After(15 * time.Second):
		log.Println("✅ 15s overhang complete. Drawer should be securely closed.")
	case <-rec.Cutover:
		log.Println("⚡ Overhang interrupted! Cashier started a new sale immediately.")
	}

	// 1. Tell FFmpeg to stop safely
	if rec.Stdin != nil {
		rec.Stdin.Write([]byte("q"))
		rec.Stdin.Close()
	}
	rec.Cmd.Wait() // Wait for the MP4 to finish rendering

	// 2. Remove from active memory
	recordingMutex.Lock()
	delete(activeRecordings, sessionID)
	recordingMutex.Unlock()

	// 3. Send to your existing Drive Engine (Inherits retention and auto-delete logic!)
	// FIX: We prepend "shift_open_" so the Drive engine routes it to the ShiftLog table!
	go uploadSecurityVideo("shift_open_"+sessionID, rec.FilePath, false, shiftID)

	log.Println("🔒 SECURITY: Morning float video sent to upload queue.")
}

// ==========================================
// 3. SHIFT CLOSING (TILL MATH)
// ==========================================

// CloseShiftRequest defines the JSON we expect from the manager counting the till
type CloseShiftRequest struct {
	ActualClosingCash float64 `json:"actual_closing_cash"`
	SessionID         string  `json:"session_id"`
	ClosingBreakdown  string  `json:"closing_breakdown"` // <-- NEW
}

// CloseShift finalizes the shift, calculates financial totals, and locks the register
func CloseShift(c *gin.Context) {
	var req CloseShiftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 1. Find the currently active shift
	var activeShift models.ShiftLog
	if err := database.DB.Where("status = ?", "open").Where("closed_at IS NULL").First(&activeShift).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No open shift found to close."})
		return
	}

	// 2. Calculate Total Sales AND Counts by Payment Method
	type PaymentSummary struct {
		PaymentMethod string
		Total         float64
		Count         int
	}
	var summaries []PaymentSummary

	database.DB.Model(&models.Sale{}).
		Select("LOWER(payment_method) as payment_method, COALESCE(SUM(total_amount), 0) as total, COUNT(id) as count").
		Where("sale_time >= ?", activeShift.OpenedAt).
		Where("status = ?", "completed").
		Group("LOWER(payment_method)").
		Scan(&summaries)

	activeShift.TotalCash = 0
	activeShift.CashCount = 0
	activeShift.TotalQR = 0
	activeShift.QRCount = 0
	activeShift.TotalCard = 0
	activeShift.CardCount = 0

	for _, s := range summaries {
		if s.PaymentMethod == "cash" {
			activeShift.TotalCash = s.Total
			activeShift.CashCount = s.Count
		} else if s.PaymentMethod == "qr" || s.PaymentMethod == "duitnow" || s.PaymentMethod == "ewallet" {
			activeShift.TotalQR += s.Total
			activeShift.QRCount += s.Count
		} else if s.PaymentMethod == "card" || s.PaymentMethod == "credit" {
			activeShift.TotalCard += s.Total
			activeShift.CardCount += s.Count
		}
	}

	// 3. Core Till Math (Cleaned up!)
	var tillPayouts float64
	database.DB.Model(&models.Expense{}).
		Where("shift_id = ? AND paid_from_till = ?", activeShift.ID, true).
		Select("COALESCE(SUM(amount), 0)").Scan(&tillPayouts)

	activeShift.ExpectedCash = activeShift.OpeningCash + activeShift.TotalCash - tillPayouts
	activeShift.ActualClosingCash = req.ActualClosingCash
	activeShift.ClosingBreakdown = req.ClosingBreakdown
	activeShift.OverShortAmount = activeShift.ActualClosingCash - activeShift.ExpectedCash

	// 4. Finalize timestamps and user
	now := time.Now()
	activeShift.ClosedAt = &now
	activeShift.Status = "closed"

	userID, exists := c.Get("userID")
	closedBy := "Unknown Staff"

	if exists {
		var user models.User
		if err := database.DB.Select("username").First(&user, userID).Error; err == nil {
			closedBy = user.Username
		}
	}
	activeShift.ClosedBy = closedBy

	// 5. Save calculations and trigger Security Upload
	if req.SessionID != "" && req.SessionID != "camera_bypassed" {
		activeShift.ClosingVideoURL = "PENDING_UPLOAD_" + req.SessionID
	} else {
		activeShift.ClosingVideoURL = "NO_VIDEO"
	}

	if err := database.DB.Save(&activeShift).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to close shift and save totals"})
		return
	}

	// --- NEW: Trigger the End-of-Day Camera Overhang ---
	if req.SessionID != "" && req.SessionID != "camera_bypassed" {
		go finalizeCloseShiftVideo(req.SessionID, activeShift.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Shift closed successfully",
		"shift":   activeShift,
	})
}

// finalizeCloseShiftVideo waits 15s watching the till count, stops the camera, and routes to Drive
func finalizeCloseShiftVideo(sessionID string, shiftID uint) {
	recordingMutex.Lock()
	rec, exists := activeRecordings[sessionID]
	if exists {
		rec.IsClosing = true // Flag it so we know it's in overhang mode
	}
	recordingMutex.Unlock()

	if !exists {
		log.Println("⚠️ SECURITY: No active recording found for End-of-Day session:", sessionID)
		return
	}

	log.Printf("🎥 SECURITY: Shift %d Closed. Starting 15s overhang for End-of-Day count...", shiftID)

	select {
	case <-time.After(15 * time.Second):
		log.Println("✅ 15s EOD overhang complete. Drawer should be securely closed.")
	case <-rec.Cutover:
		log.Println("⚡ Overhang interrupted!")
	}

	if rec.Stdin != nil {
		rec.Stdin.Write([]byte("q"))
		rec.Stdin.Close()
	}
	rec.Cmd.Wait()

	recordingMutex.Lock()
	delete(activeRecordings, sessionID)
	recordingMutex.Unlock()

	// Prepend "shift_close_" so the Drive engine routes it to the correct DB column
	go uploadSecurityVideo("shift_close_"+sessionID, rec.FilePath, false, shiftID)

	log.Println("🔒 SECURITY: End-of-Day count video sent to upload queue.")
}

// ==========================================
// 4. SHIFT HISTORY (AUDIT LEDGER)
// ==========================================

// GetShiftHistory fetches all shift records for the manager's audit page
func GetShiftHistory(c *gin.Context) {
	var shifts []models.ShiftLog

	if err := database.DB.Order("opened_at desc").Find(&shifts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch shift history"})
		return
	}

	// --- NEW: Calculate LIVE running totals for the currently open shift! ---
	for i := range shifts {
		if shifts[i].Status == "open" {
			type PaymentSummary struct {
				PaymentMethod string
				Total         float64
				Count         int // <-- NEW: Catch the live count
			}
			var summaries []PaymentSummary

			database.DB.Model(&models.Sale{}).
				Select("LOWER(payment_method) as payment_method, COALESCE(SUM(total_amount), 0) as total, COUNT(id) as count").
				Where("sale_time >= ?", shifts[i].OpenedAt).
				Where("status = ?", "completed").
				Group("LOWER(payment_method)").
				Scan(&summaries)

			var liveCash, liveQR, liveCard float64
			var liveCashCount, liveQRCount, liveCardCount int // <-- NEW

			for _, s := range summaries {
				if s.PaymentMethod == "cash" {
					liveCash = s.Total
					liveCashCount = s.Count // <-- NEW
				} else if s.PaymentMethod == "qr" || s.PaymentMethod == "duitnow" || s.PaymentMethod == "ewallet" {
					liveQR += s.Total
					liveQRCount += s.Count // <-- NEW
				} else if s.PaymentMethod == "card" || s.PaymentMethod == "credit" {
					liveCard += s.Total
					liveCardCount += s.Count // <-- NEW
				}
			}

			// Temporarily inject the live numbers and counts into the JSON response
			shifts[i].TotalCash = liveCash
			shifts[i].CashCount = liveCashCount // <-- NEW
			shifts[i].TotalQR = liveQR
			shifts[i].QRCount = liveQRCount // <-- NEW
			shifts[i].TotalCard = liveCard
			shifts[i].CardCount = liveCardCount // <-- NEW
			var livePayouts float64
			database.DB.Model(&models.Expense{}).
				Where("shift_id = ? AND paid_from_till = ?", shifts[i].ID, true).
				Select("COALESCE(SUM(amount), 0)").Scan(&livePayouts)

			shifts[i].ExpectedCash = shifts[i].OpeningCash + liveCash - livePayouts
		}
	}
	// ------------------------------------------------------------------------

	c.JSON(http.StatusOK, shifts)
}

// GetLastClosedShift fetches the absolute most recently closed shift.
// This powers the "Load Previous Till Count" hot-swap feature for rapid shift changes.
func GetLastClosedShift(c *gin.Context) {
	var lastShift models.ShiftLog

	// Query the database for a closed shift, ordered by the closing time descending (newest first)
	err := database.DB.Where("status = ?", "closed").Order("closed_at desc").First(&lastShift).Error

	if err != nil {
		// A 404 simply means this is the very first time the system is being used, or no shifts exist.
		c.JSON(http.StatusNotFound, gin.H{"error": "No previously closed shift found."})
		return
	}

	// Return the shift data (which includes ActualClosingCash and ClosingBreakdown JSON)
	c.JSON(http.StatusOK, lastShift)
}

// --- NEW: SHIFT ANALYTICS ENGINE ---

type ShiftAnalyticsResponse struct {
	TotalRevenue  float64          `json:"total_revenue"`
	TrueProfit    float64          `json:"true_profit"`
	TotalOrders   int64            `json:"total_orders"`
	AvgOrderValue float64          `json:"avg_order_value"`
	VoidCount     int64            `json:"void_count"`
	VoidValue     float64          `json:"void_value"`
	TillPayouts   []models.Expense `json:"till_payouts"` // <-- NEW: Fetch the payouts for the UI
}

// GetShiftAnalytics calculates KPIs strictly fenced to a specific shift's timeframe
func GetShiftAnalytics(c *gin.Context) {
	shiftID := c.Param("id")
	var shift models.ShiftLog

	// 1. Find the specific shift
	if err := database.DB.First(&shift, shiftID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shift not found"})
		return
	}

	// 2. Establish the timeframe boundary
	endTime := time.Now()
	if shift.ClosedAt != nil {
		endTime = *shift.ClosedAt
	}

	// 3. Calculate Sales, Revenue, and True Profit
	var sales []models.Sale
	database.DB.Preload("Items").
		Where("sale_time >= ? AND sale_time <= ? AND status = ?", shift.OpenedAt, endTime, "completed").
		Find(&sales)

	var totalRevenue, trueProfit float64
	totalOrders := int64(len(sales))

	for _, sale := range sales {
		totalRevenue += sale.TotalAmount
		// Profit is exactly: (Sell Price - Cost Price) * Quantity
		for _, item := range sale.Items {
			trueProfit += float64(item.Quantity) * (item.PriceAtSale - item.BuyPriceRM)
		}
	}

	avgOrderValue := float64(0)
	if totalOrders > 0 {
		avgOrderValue = totalRevenue / float64(totalOrders)
	}

	// 4. Calculate Voids during this exact shift
	var voidCount int64
	var voidValue float64

	database.DB.Table("voided_transactions").
		Where("timestamp >= ? AND timestamp <= ?", shift.OpenedAt, endTime).
		Count(&voidCount)

	database.DB.Table("voided_transactions").
		Where("timestamp >= ? AND timestamp <= ?", shift.OpenedAt, endTime).
		Select("COALESCE(SUM(total_value_lost), 0)").
		Scan(&voidValue)

	// 5. Fetch Till Payouts
	var payouts []models.Expense
	database.DB.Where("shift_id = ? AND paid_from_till = ?", shift.ID, true).Find(&payouts)

	// 6. Send the compiled KPIs back to React
	c.JSON(http.StatusOK, ShiftAnalyticsResponse{
		TotalRevenue:  totalRevenue,
		TrueProfit:    trueProfit,
		TotalOrders:   totalOrders,
		AvgOrderValue: avgOrderValue,
		VoidCount:     voidCount,
		VoidValue:     voidValue,
		TillPayouts:   payouts, // <-- NEW
	})
}
