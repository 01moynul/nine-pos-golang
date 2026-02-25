package handlers

import (
	"net/http"

	"go-pos-agent/internal/auth"
	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func Login(c *gin.Context) {
	var input LoginRequest
	// 1. Validate Input JSON
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 2. Find User in DB
	var user models.User
	if err := database.DB.Where("username = ?", input.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// 3. Verify Password (Bcrypt)
	// This compares the input "password" with the "hash" from DB
	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// 4. Generate JWT Token
	token, err := auth.GenerateToken(user.ID, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	// 5. Success! Return Token and Role
	c.JSON(http.StatusOK, gin.H{
		"token":    token,
		"role":     user.Role,
		"username": user.Username,
	})
}

// --- ADD THIS AT THE BOTTOM ---

func Register(c *gin.Context) {
	var input LoginRequest

	// 1. Parse JSON
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	// 2. Hash the Password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	// 3. Create User Model
	user := models.User{
		Username:     input.Username,
		PasswordHash: string(hashedPassword),
		Role:         "admin", // Defaulting to admin for now so you can test
	}

	// 4. Save to DB
	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "User likely already exists"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "User created successfully!"})
}
