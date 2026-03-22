package handlers

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time" // <-- ADDED: For tracking when the drawer opened

	"go-pos-agent/internal/database" // <-- ADDED: To save the audit log
	"go-pos-agent/internal/models"   // <-- ADDED: For the DrawerActivityLog struct

	"github.com/gin-gonic/gin"
)

// KickDrawer acts as the "Phantom Receipt", sending an ESC/POS command
// to open the physical cash drawer without triggering the thermal printhead.
func KickDrawer(c *gin.Context) {
	// Standard ESC/POS drawer kick command: ESC p 0 25 250
	kickCommand := []byte{27, 112, 0, 25, 250}

	// 1. Create a temporary binary file to hold the raw bytes
	tempFile := filepath.Join(os.TempDir(), "drawer_kick.bin")
	err := os.WriteFile(tempFile, kickCommand, 0644)
	if err != nil {
		log.Printf("❌ Error writing temp kick file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create hardware trigger file"})
		return
	}

	// 2. Read the shared printer name from .env
	printerName := os.Getenv("RECEIPT_PRINTER")
	if printerName == "" {
		printerName = "POSPrinter" // Default fallback if .env is missing
	}

	// 3. Construct the Windows network share path (e.g., \\127.0.0.1\POSPrinter)
	sharePath := fmt.Sprintf(`\\127.0.0.1\%s`, printerName)

	// 4. Use Windows CMD to copy the raw binary file directly to the printer port
	// Command: copy /b drawer_kick.bin \\127.0.0.1\POSPrinter
	cmd := exec.Command("cmd", "/C", "copy", "/b", tempFile, sharePath)

	if err := cmd.Run(); err != nil {
		log.Printf("❌ Error sending raw command to Windows Printer Spooler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to communicate with printer driver"})
		return
	}

	log.Printf("🔌 Hardware Trigger: Successfully sent Kick Command to %s", sharePath)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Cash drawer open command executed via OS spooler",
	})
}

// OpenDrawerSecurely handles audited manual drawer opens with camera tracking
func OpenDrawerSecurely(c *gin.Context) {
	var req struct {
		Reason           string `json:"reason"`
		SecurityVideoURL string `json:"security_video_url"` // Will hold PENDING_UPLOAD...
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	// 1. Identify the Staff Member
	var staffID uint
	username := "Unknown Staff"

	if uid, exists := c.Get("userID"); exists {
		// Handle JWT float64 conversion safely
		if v, ok := uid.(float64); ok {
			staffID = uint(v)
		} else if v, ok := uid.(uint); ok {
			staffID = v
		}

		// Lookup real username
		var user models.User
		if err := database.DB.Select("username").First(&user, staffID).Error; err == nil {
			username = user.Username
		}
	}

	// 2. Save the Audit Record to Database
	logEntry := models.DrawerActivityLog{
		StaffID:   staffID,
		Username:  username,
		Reason:    req.Reason,
		Status:    "Completed", // If they cancel in React, it triggers StopVoid instead
		Timestamp: time.Now(),
		// We use a dynamic map to update this safely below just in case the struct is strictly typed
	}

	if err := database.DB.Create(&logEntry).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log drawer activity"})
		return
	}

	// Attach the pending video URL
	database.DB.Model(&logEntry).Update("security_video_url", req.SecurityVideoURL)

	// 3. Kick the Physical Drawer (Reusing your proven ESC/POS logic)
	kickCommand := []byte{27, 112, 0, 25, 250}
	tempFile := filepath.Join(os.TempDir(), "secure_drawer_kick.bin")
	os.WriteFile(tempFile, kickCommand, 0644)

	printerName := os.Getenv("RECEIPT_PRINTER")
	if printerName == "" {
		printerName = "POSPrinter"
	}

	sharePath := fmt.Sprintf(`\\127.0.0.1\%s`, printerName)
	cmd := exec.Command("cmd", "/C", "copy", "/b", tempFile, sharePath)

	if err := cmd.Run(); err != nil {
		log.Printf("❌ HARDWARE ERROR: Failed to kick drawer for audit %d: %v", logEntry.ID, err)
		// We still return 200 because the DB logged it, but we warn the frontend
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Logged, but hardware failed to open", "log_id": logEntry.ID})
		return
	}

	log.Printf("🔌 SECURE HARDWARE: Drawer kicked by %s (Reason: %s)", username, req.Reason)

	// 4. Return success and the new Log ID (React needs this ID to stop the camera!)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Drawer opened and logged securely",
		"log_id":  logEntry.ID,
	})
}
