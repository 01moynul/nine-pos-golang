package database

import (
	"log"
	"os" // <--- Added this to read environment variables
	"time"

	"go-pos-agent/internal/models"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Connect() {
	// 1. Get Credentials from .env file
	// This makes the app portable!
	dsn := os.Getenv("DB_DSN")

	if dsn == "" {
		log.Fatal("❌ Error: DB_DSN not found in .env file. Please configure your database.")
	}

	var err error

	// 2. Connect with GORM (Wait for DB to be ready)
	for i := 0; i < 5; i++ {
		DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err == nil {
			break
		}
		log.Printf("Failed to connect to database. Retrying in 2 seconds... (%d/5)", i+1)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		log.Fatal("Failed to connect to database after 5 attempts:", err)
	}

	log.Println("✅ Successfully connected to MySQL!")

	// 3. Auto-Migrate
	err = DB.AutoMigrate(
		&models.User{},
		&models.Product{},
		&models.ComboComponent{},
		&models.Sale{},
		&models.SaleItem{},
		&models.AuditLog{},
	)

	log.Println("✅ Database Schema Synced!")
}
