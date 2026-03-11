package handlers

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

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
