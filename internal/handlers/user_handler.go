package handlers

import (
	"net/http"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// GetUsers returns all users (hiding passwords from the response)
func GetUsers(c *gin.Context) {
	var users []models.User
	// Only select safe fields to send to the frontend
	if err := database.DB.Select("id, username, role, created_at").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch users"})
		return
	}
	c.JSON(http.StatusOK, users)
}

// CreateUserRequest defines the payload for creating a user
type CreateUserRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role" binding:"required"`
}

// CreateUser adds a new user to the system via the Admin Dashboard
func CreateUser(c *gin.Context) {
	var input CreateUserRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user := models.User{
		Username:     input.Username,
		PasswordHash: string(hashedPassword),
		Role:         input.Role,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username already exists"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "User created successfully!"})
}

// UpdateUserRequest defines the payload for updating a user
type UpdateUserRequest struct {
	Role     string `json:"role"`
	Password string `json:"password"` // Optional: only update if provided
}

// UpdateUser changes a user's role or password
func UpdateUser(c *gin.Context) {
	id := c.Param("id")
	var input UpdateUserRequest

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid input"})
		return
	}

	var user models.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Safeguard: Prevent changing the master admin's role
	if user.Username == "admin" && input.Role != "" && input.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot change the master admin's role"})
		return
	}

	// Update fields if they were provided
	if input.Role != "" {
		user.Role = input.Role
	}
	if input.Password != "" {
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash new password"})
			return
		}
		user.PasswordHash = string(hashedPassword)
	}

	database.DB.Save(&user)
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

// DeleteUser removes a user from the system
func DeleteUser(c *gin.Context) {
	id := c.Param("id")

	var user models.User
	if err := database.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	// Safeguard: Never delete the master admin
	if user.Username == "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot delete the master admin account"})
		return
	}

	database.DB.Delete(&user)
	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}
