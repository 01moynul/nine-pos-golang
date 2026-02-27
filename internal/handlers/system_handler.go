package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"
	"go-pos-agent/internal/utils" // Import our new hardware utility

	"github.com/gin-gonic/gin"
)

type LicenseRequest struct {
	LicenseKey string `json:"license_key" binding:"required"`
}

// GetSystemStatus feeds the Lockdown screen the Device ID so the client can text it to you
func GetSystemStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"device_id": utils.GetDeviceID(),
	})
}

// ActivateLicense checks the provided key against the 5 contract stages mapped to this exact hardware
func ActivateLicense(c *gin.Context) {
	var req LicenseRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// 1. Get the physical Hardware ID of THIS specific computer
	currentDeviceID := utils.GetDeviceID()

	// 2. The Super Secret Agency Salt (You will use this in your Keygen tool too!)
	secretSalt := "ZO-TOTAL-MASTER-SECRET-2026"

	// 3. Define the 5 Contract Stages and their strict Expiration Dates
	stages := map[string]time.Time{
		"INIT":     time.Date(2026, 3, 7, 23, 59, 59, 0, time.Local),   // Initial Setup -> Expires March 7
		"MARCH":    time.Date(2026, 4, 7, 23, 59, 59, 0, time.Local),   // Installment 1 -> Expires April 7
		"APRIL":    time.Date(2026, 5, 7, 23, 59, 59, 0, time.Local),   // Installment 2 -> Expires May 7
		"MAY":      time.Date(2026, 6, 7, 23, 59, 59, 0, time.Local),   // Installment 3 -> Expires June 7
		"LIFETIME": time.Date(2099, 12, 31, 23, 59, 59, 0, time.Local), // Final Installment -> Unlimited Single-Device
	}

	var matchedExpiration time.Time
	var matchedStage string
	isValid := false

	// 4. Verify the Key against all possible stages for THIS hardware
	for stage, expDate := range stages {
		// Calculate what the key SHOULD look like for this specific stage and hardware
		hash := sha256.Sum256([]byte(currentDeviceID + stage + secretSalt))
		expectedKey := stage + "-" + strings.ToUpper(hex.EncodeToString(hash[:])[:12])

		if req.LicenseKey == expectedKey {
			isValid = true
			matchedExpiration = expDate
			matchedStage = stage
			break
		}
	}

	if !isValid {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid key for this specific device. Please contact Zo Total Software Solutions.",
		})
		return
	}

	// 5. The key is perfectly matched! Update the database.
	var license models.SystemLicense
	database.DB.First(&license)

	license.LicenseKey = req.LicenseKey
	license.ExpirationDate = matchedExpiration
	license.IsActive = true

	if license.ID == 0 {
		database.DB.Create(&license)
	} else {
		database.DB.Save(&license)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "System Reactivated! Stage: " + matchedStage,
		"expires": license.ExpirationDate,
	})
}
