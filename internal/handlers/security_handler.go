package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// ActiveRecording tracks ongoing camera sessions in server memory
type ActiveRecording struct {
	SessionID string
	Cmd       *exec.Cmd // The actual FFmpeg process
	FilePath  string    // Where the temporary video is saving
	IsClosing bool      // Flag for our 30-second overhang logic later
}

var (
	// Map to securely track active recordings by SessionID
	activeRecordings = make(map[string]*ActiveRecording)
	// Mutex prevents server crashes if multiple cashiers trigger the camera at the exact same millisecond
	recordingMutex sync.Mutex
)

// StartRecording API - Triggered the moment the cart goes from 0 to 1 item
func StartRecording(c *gin.Context) {
	recordingMutex.Lock()
	defer recordingMutex.Unlock()

	// 1. Generate a unique ID for this cart session
	sessionID := uuid.New().String()

	// 2. Define where the temporary video will be saved locally before cloud upload
	tempDir := filepath.Join("C:\\NinePOS_Data", "temp_security_vids")
	os.MkdirAll(tempDir, os.ModePerm) // Ensure folder exists
	fileName := fmt.Sprintf("session_%s.mp4", sessionID)
	filePath := filepath.Join(tempDir, fileName)

	// 3. Build the FFmpeg Command
	// This captures the Windows Desktop (gdigrab) and a specific Webcam (dshow).
	// We use a low framerate (10 FPS) to keep CPU usage extremely low.
	// NOTE: Ensure your webcam is named exactly "Security_Cam" in Windows, or change it here.
	cmd := exec.Command("./ffmpeg.exe",
		"-y",                                                 // Overwrite output files without asking
		"-f", "gdigrab", "-framerate", "10", "-i", "desktop", // Capture Screen
		"-f", "dshow", "-framerate", "10", "-i", "video=Logitech BRIO", // Capture Webcam <--- MAPPED HERE!
		"-filter_complex", "[0:v][1:v] overlay=W-w-10:H-h-10", // Picture-in-Picture (Bottom Right)
		"-vcodec", "libx264", "-preset", "ultrafast", "-crf", "28", // Heavy compression for speed
		filePath,
	)

	// 4. Start the camera silently in the background
	err := cmd.Start()
	if err != nil {
		log.Printf("⚠️ SECURITY WARNING: Failed to start FFmpeg. Camera might be in use or unplugged. Error: %v\n", err)
		// We still return 200 OK so the cashier can keep checking out the customer. We do not freeze the POS.
		c.JSON(http.StatusOK, gin.H{"status": "camera_bypassed", "session_id": sessionID})
		return
	}

	// 5. Save the active session into our server memory
	activeRecordings[sessionID] = &ActiveRecording{
		SessionID: sessionID,
		Cmd:       cmd,
		FilePath:  filePath,
		IsClosing: false,
	}

	log.Printf("🔴 SECURITY REC: Started recording session %s\n", sessionID)

	// 6. Return the SessionID to React so it can lock the cart to this video
	c.JSON(http.StatusOK, gin.H{
		"message":    "Recording started successfully",
		"session_id": sessionID,
	})
}

// LogRemoval API - Silently logs when a cashier drops a single item to the trash can
func LogRemoval(c *gin.Context) {
	// 1. Define the expected payload from React
	var req struct {
		SessionID string `json:"session_id"`
		ItemName  string `json:"item_name"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	// 2. Build the database entry (Notice we are using time.Now() here!)
	logEntry := models.SuspiciousActivityLog{
		SessionID: req.SessionID,
		// Note: We will dynamically grab the UserID from the JWT middleware later
		Action:    "PARTIAL_LINE_VOID",
		ItemName:  req.ItemName,
		Timestamp: time.Now(), // <--- This clears the "time" import error!
	}

	// 3. Save the ping silently to the database
	database.DB.Create(&logEntry)

	// 4. Return success to React without stopping the video
	c.JSON(http.StatusOK, gin.H{"status": "partial_void_logged"})
}

// StopSuccess API - Triggered when the cashier successfully clicks "Pay & Print"
func StopSuccess(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		OrderID   uint   `json:"order_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	// Hand off to the background Goroutine so React doesn't freeze waiting for the camera
	go finalizeRecording(req.SessionID, true, req.OrderID, "", 0, "")

	c.JSON(http.StatusOK, gin.H{"status": "finalizing_success_in_background"})
}

// StopVoid API - Triggered when "Clear Order" is clicked or the cart is manually emptied
func StopVoid(c *gin.Context) {
	var req struct {
		SessionID      string  `json:"session_id"`
		Reason         string  `json:"reason"`
		TotalValueLost float64 `json:"total_value_lost"`
		ItemsInCart    string  `json:"items_in_cart"` // Expecting a JSON string of the cart
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	go finalizeRecording(req.SessionID, false, 0, req.Reason, req.TotalValueLost, req.ItemsInCart)

	c.JSON(http.StatusOK, gin.H{"status": "finalizing_void_in_background"})
}

// finalizeRecording - The Background Engine handling the 30-Second Overhang
func finalizeRecording(sessionID string, isSuccess bool, orderID uint, reason string, valueLost float64, items string) {
	// 1. Find the active session in server memory
	recordingMutex.Lock()
	session, exists := activeRecordings[sessionID]
	if !exists {
		recordingMutex.Unlock()
		return
	}
	session.IsClosing = true
	recordingMutex.Unlock()

	log.Printf("⏱️ SECURITY: Transaction closed. Starting 30-second camera overhang for Session %s...\n", sessionID)

	// 2. THE OVERHANG: Wait exactly 30 seconds to catch cash handling!
	time.Sleep(30 * time.Second)

	// 3. Stop the camera
	recordingMutex.Lock()
	if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill() // Safely force quit FFmpeg
	}
	delete(activeRecordings, sessionID) // Clear from server memory
	recordingMutex.Unlock()

	log.Printf("🛑 SECURITY: Camera stopped for Session %s. Saving to database...\n", sessionID)

	// 4. Save to the correct database table based on whether it was a Success or Void
	if isSuccess {
		// Update the existing Order with the video URL (URL will be generated in Step 4 Cloud Upload)
		database.DB.Model(&models.Sale{}).Where("id = ?", orderID).Update("security_video_url", "PENDING_UPLOAD_"+sessionID)
	} else {
		// Create a brand new record in the VoidedTransactions table
		voidRecord := models.VoidedTransaction{
			SessionID:        sessionID,
			TotalValueLost:   valueLost,
			ItemsInCart:      items,
			Reason:           reason,
			SecurityVideoURL: "PENDING_UPLOAD_" + sessionID,
			Timestamp:        time.Now(),
		}
		database.DB.Create(&voidRecord)
	}

	// 5. Trigger the Cloud Upload Process
	uploadSecurityVideo(sessionID, session.FilePath, isSuccess, orderID)
}

// uploadSecurityVideo handles the Google Drive upload and database linking
func uploadSecurityVideo(sessionID string, localFilePath string, isSuccess bool, orderID uint) {
	log.Printf("☁️ SECURITY: Starting cloud upload for Session %s...\n", sessionID)

	// 1. Get the dedicated Security Folder ID from .env
	folderID := os.Getenv("SECURITY_DRIVE_FOLDER_ID")
	if folderID == "" {
		log.Println("❌ SECURITY ERROR: SECURITY_DRIVE_FOLDER_ID is missing in .env")
		return
	}

	// 2. Authenticate using the existing credentials.json
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Google Drive auth failed: %v\n", err)
		return
	}

	// 3. Open the local .mp4 file
	file, err := os.Open(localFilePath)
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Could not open local video file: %v\n", err)
		return
	}
	defer file.Close()

	// 4. Define Google Drive metadata (Name and Folder)
	cloudFileName := filepath.Base(localFilePath)
	f := &drive.File{
		Name:    cloudFileName,
		Parents: []string{folderID},
	}

	// 5. Upload to Google Drive (Using SupportsAllDrives to bypass Workspace limits)
	uploadedFile, err := client.Files.Create(f).
		Media(file).
		SupportsAllDrives(true).
		Fields("id, webViewLink"). // We specifically request the shareable link!
		Do()

	if err != nil {
		log.Printf("❌ SECURITY ERROR: Google Drive upload failed: %v\n", err)
		return
	}

	// 6. Set File Permissions to "Anyone with the link can view"
	permission := &drive.Permission{
		Type: "anyone",
		Role: "reader",
	}
	_, err = client.Permissions.Create(uploadedFile.Id, permission).
		SupportsAllDrives(true).
		Do()

	if err != nil {
		log.Printf("⚠️ SECURITY WARNING: Uploaded, but failed to set public permissions: %v\n", err)
	}

	// 7. Update the Database with the new Video Link!
	videoLink := uploadedFile.WebViewLink
	searchStr := "PENDING_UPLOAD_" + sessionID

	if isSuccess {
		database.DB.Model(&models.Sale{}).Where("id = ?", orderID).Update("security_video_url", videoLink)
	} else {
		// Because Voided transactions might not have a clean ID yet, we find by the SessionID placeholder
		database.DB.Model(&models.VoidedTransaction{}).Where("security_video_url = ?", searchStr).Update("security_video_url", videoLink)
	}

	log.Printf("✅ SECURITY: Upload complete! Link saved to database. Deleting local file...\n")

	// 8. Delete the local .mp4 to save hard drive space on the POS
	file.Close() // Ensure Windows unlocks the file
	os.Remove(localFilePath)
}
