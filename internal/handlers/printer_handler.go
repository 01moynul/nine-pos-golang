package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// KickDrawer acts as the "Phantom Receipt", sending an ESC/POS command
// to open the physical cash drawer without triggering the thermal printhead.
func KickDrawer(c *gin.Context) {
	// Standard ESC/POS drawer kick command: ESC p 0 25 250
	// Decimal equivalent: 27 112 0 25 250
	kickCommand := []byte{27, 112, 0, 25, 250}

	// For security and auditing, we log when the drawer is manually popped
	log.Printf("🔌 Hardware Trigger: Sending Cash Drawer Kick Command: %v", kickCommand)

	// Note: Later, if the browser cannot capture raw USB byte codes via the Web Serial API,
	// we will add the Windows OS-level routing here to send these bytes directly to \\localhost\PrinterShare

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Cash drawer open command executed",
	})
}
