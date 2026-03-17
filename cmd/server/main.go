package main

import (
	"log"
	"os"
	"time" // <--- 1. ADD THIS

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/handlers"
	"go-pos-agent/internal/middleware"

	"github.com/gin-contrib/cors" // <--- 2. ADD THIS (The Bridge Library)
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: No .env file found")
	}

	// Connect to Database
	database.Connect()

	// --- NEW FIX: Auto-Recover stranded security videos on boot ---
	go handlers.RetryFailedUploads()
	// --------------------------------------------------------------

	// --- NEW: BACKGROUND BACKUP ENGINE ---
	// 1. Hourly Auto-Backup
	go func() {
		hourlyTicker := time.NewTicker(1 * time.Hour)
		for range hourlyTicker.C {
			handlers.RunHourlyAutoBackup()
		}
	}()

	// 2. Daily Cleanup (Deletes auto-backups older than 7 days)
	go func() {
		dailyTicker := time.NewTicker(24 * time.Hour)
		for range dailyTicker.C {
			handlers.CleanupOldAutoBackups()
		}
	}()
	// -------------------------------------

	r := gin.Default()

	// --- 3. ADD THIS BLOCK HERE (The Bridge Configuration) ---
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173"}, // Allow React
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))
	// -------------------------------------------------------

	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "online"}) })
	r.POST("/login", handlers.Login)
	r.Static("/uploads", "./uploads")

	// 🚨 UNLOCKED ROUTE: System Activation must bypass the DRM!
	r.GET("/api/system/status", handlers.GetSystemStatus)
	r.POST("/api/system/activate", handlers.ActivateLicense)

	// --- FEATURE FLAG: Admin Registration ---
	// Only opens if we explicitly allow it in .env
	if os.Getenv("ALLOW_REGISTRATION") == "true" {
		r.POST("/register", handlers.Register)
		log.Println("⚠️ WARNING: Registration route is OPEN. Disable this in production!")
	} else {
		log.Println("🔒 Registration route is safely DISABLED.")
	}

	// --- PROTECTED ROUTES ---
	api := r.Group("/api")
	api.Use(middleware.CheckLicense())
	api.Use(middleware.AuthMiddleware())
	{
		// PUBLIC TO STAFF & ADMIN
		api.GET("/products", handlers.GetProducts)
		api.POST("/checkout", handlers.ProcessSale)
		api.GET("/products/scan/:barcode", handlers.ScanProduct)
		// --- NEW: SMART SECURITY ROUTES (Task 2.4) ---
		security := api.Group("/security")
		// --- ADD THIS NEW LINE ---
		api.POST("/printer/kick-drawer", handlers.KickDrawer)

		// --- NEW: TILL MANAGEMENT (Shift Logs) ---
		api.GET("/settings", handlers.GetStoreSettings)
		api.GET("/shift/active", handlers.GetActiveShift)
		api.GET("/shift/history", handlers.GetShiftHistory) // <--- ADD THIS NEW LINE
		api.POST("/shift/unlock", handlers.UnlockRegister)
		api.POST("/shift/open", handlers.OpenShift)
		api.POST("/shift/close", handlers.CloseShift)
		// ------------------------------------------

		{
			security.POST("/start", handlers.StartRecording)
			security.POST("/log-removal", handlers.LogRemoval)
			security.POST("/stop-success", handlers.StopSuccess)
			security.POST("/stop-void", handlers.StopVoid)
		}
		// ----------------------------------------------

		// --- MANAGEMENT ONLY (Admin + Supervisor) ---
		management := api.Group("/")
		management.Use(middleware.RequireRoles("admin", "supervisor"))
		{
			management.POST("/upload", handlers.UploadImage)
			management.POST("/products", handlers.AddProduct)
			management.PUT("/products/:id", handlers.UpdateProduct)
			management.GET("/reports/valuation", handlers.GetStockValuation) // Inventory Report
		}

		// --- ADMIN ONLY (Strict Financials & Deletions) ---
		admin := api.Group("/")
		admin.Use(middleware.RequireRoles("admin"))
		{
			// AI is restricted to Admin
			admin.POST("/ask", handlers.AskAI)

			admin.DELETE("/products/:id", handlers.DeleteProduct) // Supervisors cannot delete
			admin.GET("/reports", handlers.GetSalesReport)
			admin.GET("/reports/valuation/history", handlers.GetHistoricalValuation)

			// Backup Management Routes
			admin.GET("/backup/list", handlers.GetBackupsList)
			admin.POST("/backup/manual", handlers.TriggerManualBackup)
			admin.DELETE("/backup/:id", handlers.DeleteBackup)
			admin.GET("/backup/download/:id", handlers.DownloadBackup)

			admin.GET("/products/scale-export", handlers.ExportWeighableProducts)

			// Shop Expenses Management
			admin.POST("/expenses", handlers.CreateExpense)
			admin.GET("/expenses", handlers.GetExpenses)
			admin.DELETE("/expenses/:id", handlers.DeleteExpense)

			// --- NEW: User Management Routes ---
			admin.GET("/users", handlers.GetUsers)
			admin.POST("/users", handlers.CreateUser)
			admin.PUT("/users/:id", handlers.UpdateUser)
			admin.DELETE("/users/:id", handlers.DeleteUser)
		}
	}

	// --- 5. DEPLOYMENT: Serve React Frontend ---
	// Serve the static files (JS, CSS, Images)
	r.Static("/assets", "./web/assets")
	r.StaticFile("/vite.svg", "./web/vite.svg")

	// SPA Catch-All: If the user refreshes on "/dashboard",
	// serve index.html so React can handle the routing.
	r.NoRoute(func(c *gin.Context) {
		c.File("./web/index.html")
	})
	// -------------------------------------------

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	log.Println("🚀 Server starting on " + baseURL)
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
