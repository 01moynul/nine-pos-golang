package database

import (
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"go-pos-agent/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Connect() {
	// 1. Define the permanent directory
	dbDir := `C:\NinePOS_Data`
	err := os.MkdirAll(dbDir, os.ModePerm)
	if err != nil {
		log.Fatal("❌ Error: Failed to create database directory:", err)
	}

	dbPath := filepath.Join(dbDir, "inventory.db")

	// 2. SQLCipher Connection String
	encryptionKey := "#^3pjwdEWt5%m$TPmW8c8YfNwK4XZ^LdY5qD5su5wg#@1GtU6Ev4TygRYw8^4xSW"

	// Safely encode the special characters!
	escapedKey := url.QueryEscape(encryptionKey)

	dsn := "file:" + dbPath + "?_pragma_key=" + escapedKey + "&_pragma_cipher_page_size=4096"

	var dbErr error

	// 3. Connect with GORM
	for i := 0; i < 5; i++ {
		DB, dbErr = gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if dbErr == nil {
			break
		}
		log.Printf("Failed to connect to database. Retrying in 2 seconds... (%d/5)", i+1)
		time.Sleep(2 * time.Second)
	}

	if dbErr != nil {
		log.Fatal("Failed to connect to database after 5 attempts:", dbErr)
	}

	log.Println("✅ Successfully connected to Encrypted SQLCipher database at", dbPath)

	// 4. Auto-Migrate
	err = DB.AutoMigrate(
		&models.User{},
		&models.Product{},
		&models.ComboComponent{},
		&models.Sale{},
		&models.SaleItem{},
		&models.AuditLog{},
	)
	if err != nil {
		log.Fatal("❌ Failed to migrate database:", err)
	}

	log.Println("✅ Database Schema Synced!")

	// 5. Seed Default Users if empty
	seedInitialUsers()
}

func seedInitialUsers() {
	var count int64
	DB.Model(&models.User{}).Count(&count)

	if count == 0 {
		log.Println("⚠️ No users found in database. Generating default accounts...")

		// Hash the default passwords
		adminHash, _ := bcrypt.GenerateFromPassword([]byte("RWZxXxaYkyK3C@@ZR$SX%RPg"), bcrypt.DefaultCost)
		staffHash, _ := bcrypt.GenerateFromPassword([]byte("q33&Lq*aMGzRC1j&j^wRub*H"), bcrypt.DefaultCost)

		// Create Master Admin
		admin := models.User{
			Username:     "admin",
			PasswordHash: string(adminHash), // <--- Change this field name!
			Role:         "admin",
		}

		// Create Default Cashier
		staff := models.User{
			Username:     "cashier",
			PasswordHash: string(staffHash), // <--- Change this field name!
			Role:         "staff",
		}

		DB.Create(&admin)
		DB.Create(&staff)

		log.Println("✅ Default Master Admin (admin/admin123) and Staff (cashier/staff123) created successfully!")
	}
}
