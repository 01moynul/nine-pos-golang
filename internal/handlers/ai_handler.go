package handlers

import (
	"net/http"
	"os"

	"go-pos-agent/internal/ai"

	"github.com/gin-gonic/gin"
)

type AskRequest struct {
	Message string `json:"message" binding:"required"`
}

func AskAI(c *gin.Context) {
	var req AskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is required"})
		return
	}

	// 1. Get API Key from Environment Variable (Security Best Practice)
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Server missing OpenAI API Key"})
		return
	}

	// 2. Run the AI Agent
	response, err := ai.RunAgent(req.Message, apiKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 3. Return the Answer
	c.JSON(http.StatusOK, gin.H{"reply": response})
}
