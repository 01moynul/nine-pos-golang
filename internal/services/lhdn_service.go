package services

import (
	"fmt"
	"time"

	"go-pos-agent/internal/models"
)

// LHDNResponse simulates the expected JSON payload from the Malaysian MyInvois API.
type LHDNResponse struct {
	ValidationID string
	QRCodeURL    string
	Status       string
}

// SubmitToLHDNSandbox is our mock engine for Task 2.2.
// It accepts a finalized Sale, pretends to format it into LHDN XML/JSON,
// and returns a successful mock response.
func SubmitToLHDNSandbox(sale models.Sale) LHDNResponse {
	// In the future, we will map sale.Items to the exact LHDN tax categories here.

	// Simulate a 500ms network delay to mimic the real MyInvois API call latency.
	// This helps us test if the frontend UI freezes during checkout.
	time.Sleep(500 * time.Millisecond)

	// Generate a mock Validation ID using the unique POS Receipt ID
	mockValidationID := fmt.Sprintf("LHDN-VAL-%s", sale.ReceiptID)

	// Generate a mock QR Code URL.
	// We use a free public API here so your thermal printer can actually render a real QR code during Sandbox testing.
	// In production, this will be the official LHDN portal link.
	mockQR := fmt.Sprintf("https://api.qrserver.com/v1/create-qr-code/?size=150x150&data=%s", mockValidationID)

	// Return the mock success payload
	return LHDNResponse{
		ValidationID: mockValidationID,
		QRCodeURL:    mockQR,
		Status:       "Valid",
	}
}
