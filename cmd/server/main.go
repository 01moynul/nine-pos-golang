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

	database.Connect()
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

		// ADMIN ONLY
		admin := api.Group("/")
		admin.Use(middleware.RequireRole("admin"))
		{
			// AI is now restricted to Admin
			admin.POST("/ask", handlers.AskAI) // <--- MOVED HERE

			admin.POST("/upload", handlers.UploadImage)
			admin.POST("/products", handlers.AddProduct)
			admin.PUT("/products/:id", handlers.UpdateProduct)
			admin.DELETE("/products/:id", handlers.DeleteProduct)
			admin.GET("/reports", handlers.GetSalesReport)
			// --- NEW: Stock Valuation Reports ---
			admin.GET("/reports/valuation", handlers.GetStockValuation)
			admin.GET("/reports/valuation/history", handlers.GetHistoricalValuation) // <--- ADD THIS LINE
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
