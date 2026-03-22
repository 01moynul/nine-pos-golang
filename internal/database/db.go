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
		&models.StockLedger{},
		&models.SystemLicense{},
		&models.VoidedTransaction{},
		&models.SuspiciousActivityLog{},
		&models.Expense{},
		// --- NEW: Till Management Tables ---
		&models.ShiftLog{},      // <--- ADD THIS LINE
		&models.StoreSettings{}, // <--- ADD THIS LINE
		&models.DrawerActivityLog{},
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

	// 1. If DB is COMPLETELY empty, create Admin and Staff
	if count == 0 {
		log.Println("⚠️ No users found in database. Generating default accounts...")

		adminHash, _ := bcrypt.GenerateFromPassword([]byte("RWZxXxaYkyK3C@@ZR$SX%RPg"), bcrypt.DefaultCost)
		staffHash, _ := bcrypt.GenerateFromPassword([]byte("q33&Lq*aMGzRC1j&j^wRub*H"), bcrypt.DefaultCost)

		admin := models.User{
			Username:     "admin",
			PasswordHash: string(adminHash),
			Role:         "admin",
		}
		staff := models.User{
			Username:     "cashier",
			PasswordHash: string(staffHash),
			Role:         "staff",
		}

		DB.Create(&admin)
		DB.Create(&staff)
		log.Println("✅ Default Master Admin and Staff created successfully!")
	}

	// 2. ALWAYS check specifically if a Supervisor exists (Fixes the missing account bug)
	var supervisorCount int64
	DB.Model(&models.User{}).Where("role = ?", "supervisor").Count(&supervisorCount)

	if supervisorCount == 0 {
		log.Println("⚠️ Supervisor account missing. Generating default supervisor...")
		supervisorHash, _ := bcrypt.GenerateFromPassword([]byte("w^5ZYwacDHJLRXmHa7*XKs5#"), bcrypt.DefaultCost)

		supervisor := models.User{
			Username:     "supervisor",
			PasswordHash: string(supervisorHash),
			Role:         "supervisor",
		}

		DB.Create(&supervisor)
		log.Println("✅ Default Supervisor created successfully!")
	}

	// 3. Seed Default Store Settings
	var settingsCount int64
	DB.Model(&models.StoreSettings{}).Count(&settingsCount)
	if settingsCount == 0 {
		defaultSettings := models.StoreSettings{
			EnableShiftTracking: true,
		}
		DB.Create(&defaultSettings)
		log.Println("✅ Default Store Settings seeded")
	}
}
