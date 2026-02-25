package database

import (
	"go-pos-agent/internal/models"
	"time"
)

// SalesReportResult holds the data the AI needs
type SalesReportResult struct {
	TotalRevenue float64
	TotalCount   int64
}

// GetSalesReport calculates sales within a specific date range
func GetSalesReport(start, end time.Time) (*SalesReportResult, error) {
	var result SalesReportResult

	// 1. Calculate Revenue
	// COALESCE ensures we get 0 instead of NULL if no sales exist
	err := DB.Model(&models.Sale{}).
		Where("sale_time BETWEEN ? AND ?", start, end).
		Select("COALESCE(SUM(total_amount), 0)").
		Scan(&result.TotalRevenue).Error

	if err != nil {
		return nil, err
	}

	// 2. Count Orders
	err = DB.Model(&models.Sale{}).
		Where("sale_time BETWEEN ? AND ?", start, end).
		Count(&result.TotalCount).Error

	if err != nil {
		return nil, err
	}

	return &result, nil
}
