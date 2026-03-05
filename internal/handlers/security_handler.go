package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings" // <--- ADD THIS
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
	Cmd       *exec.Cmd      // The actual FFmpeg process
	Stdin     io.WriteCloser // <--- NEW FIX: Pipe to send commands to FFmpeg
	FilePath  string         // Where the temporary video is saving
	IsClosing bool           // Flag for our 30-second overhang logic later
	Cutover   chan bool      // <--- NEW FIX: Channel to interrupt the 30s timer
}

var (
	// Map to securely track active recordings by SessionID
	activeRecordings = make(map[string]*ActiveRecording)
	// Mutex prevents server crashes if multiple cashiers trigger the camera at the exact same millisecond
	recordingMutex sync.Mutex
)

// StartRecording API - Triggered the moment the cart goes from 0 to 1 item
func StartRecording(c *gin.Context) {
	// --- NEW FIX: The Instant Cutover Interceptor ---
	// We must safely stop any existing recordings and free the hardware BEFORE starting a new one.
	recordingMutex.Lock()
	var pendingSessions []*ActiveRecording
	for _, session := range activeRecordings {
		pendingSessions = append(pendingSessions, session)
	}
	recordingMutex.Unlock()

	if len(pendingSessions) > 0 {
		log.Println("⚡ SECURITY: Instant Cutover triggered! Intercepting previous camera session...")
		for _, session := range pendingSessions {
			if session.IsClosing && session.Cutover != nil {
				// It's currently in the 30-second sleep. Wake it up instantly!
				select {
				case session.Cutover <- true:
				default:
				}
			} else if session.Stdin != nil {
				// Safety fallback: It was abandoned. Kill it manually.
				io.WriteString(session.Stdin, "q")
				session.Stdin.Close()
			}
		}
		// Give Windows 1.5 seconds to fully release the webcam hardware lock
		time.Sleep(1500 * time.Millisecond)
	}
	// ------------------------------------------------

	recordingMutex.Lock()
	defer recordingMutex.Unlock()

	// 1. Generate a unique ID for this cart session...
	sessionID := uuid.New().String()

	// 2. Define where the temporary video will be saved locally before cloud upload
	tempDir := filepath.Join("C:\\NinePOS_Data", "temp_security_vids")
	os.MkdirAll(tempDir, os.ModePerm) // Ensure folder exists
	fileName := fmt.Sprintf("session_%s.mp4", sessionID)
	filePath := filepath.Join(tempDir, fileName)

	// 3. Build the FFmpeg Command
	// Dynamically pull the camera name from the .env file.
	webcamName := os.Getenv("WEBCAM_DEVICE_NAME")
	if webcamName == "" {
		// Fallback for your local development if the .env variable is missing
		webcamName = "Logitech BRIO"
		log.Println("⚠️ SECURITY WARNING: WEBCAM_DEVICE_NAME not found in .env, falling back to default.")
	}
	cameraInput := "video=" + webcamName

	cmd := exec.Command("./ffmpeg.exe",
		"-y",
		"-f", "gdigrab", "-framerate", "24", "-i", "desktop",
		"-f", "dshow", "-framerate", "24", "-i", cameraInput, // <--- NEW FIX: Dynamic hardware injection
		"-filter_complex", "[0:v][1:v] overlay=W-w-10:H-h-10",
		"-vcodec", "libx264", "-preset", "ultrafast", "-crf", "28",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		filePath,
	)

	// 4. Create the Stdin pipe to send commands later, then start the camera
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("⚠️ SECURITY WARNING: Failed to create input pipe for FFmpeg: %v\n", err)
	}

	err = cmd.Start()
	if err != nil {
		log.Printf("⚠️ SECURITY WARNING: Failed to start FFmpeg. Error: %v\n", err)
		c.JSON(http.StatusOK, gin.H{"status": "camera_bypassed", "session_id": sessionID})
		return
	}

	// 5. Save the active session into our server memory
	activeRecordings[sessionID] = &ActiveRecording{
		SessionID: sessionID,
		Cmd:       cmd,
		Stdin:     stdin,
		FilePath:  filePath,
		IsClosing: false,
		Cutover:   make(chan bool, 1), // <--- Initialize the channel
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

	// 2. THE OVERHANG: Wait exactly 30 seconds... OR until interrupted by the Cutover!
	select {
	case <-time.After(30 * time.Second):
		log.Printf("⏱️ SECURITY: 30-second overhang complete for Session %s.\n", sessionID)
	case <-session.Cutover:
		log.Printf("⚡ SECURITY: Overhang interrupted! Saving Session %s instantly.\n", sessionID)
	}

	// 3. Stop the camera GRACEFULLY
	recordingMutex.Lock()
	if session.Cmd != nil && session.Stdin != nil {
		log.Printf("🛑 SECURITY: Sending graceful quit signal to FFmpeg for Session %s...\n", sessionID)

		// Send the "q" character to FFmpeg to tell it to finalize the MP4 file
		io.WriteString(session.Stdin, "q")
		session.Stdin.Close()

		// We MUST wait for FFmpeg to finish writing the file trailer.
		// We unlock the mutex temporarily so we don't freeze other POS operations while waiting.
		recordingMutex.Unlock()
		session.Cmd.Wait()
		recordingMutex.Lock() // Re-lock to safely delete from memory
	} else if session.Cmd != nil && session.Cmd.Process != nil {
		// Fallback just in case the pipe failed
		session.Cmd.Process.Kill()
	}

	delete(activeRecordings, sessionID) // Clear from server memory
	recordingMutex.Unlock()

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

	// 2. Locate the credentials file (Dynamic Pathing for Dev vs Prod)
	credPath := "credentials.json" // Production path (expects it right next to NinePOS.exe)

	// Fallback: If we are in local development, use the deep folder path
	if _, err := os.Stat("cmd/server/credentials.json"); err == nil {
		credPath = "cmd/server/credentials.json"
	}

	// 3. Authenticate using the dynamic path
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile(credPath))
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Google Drive auth failed: %v\n", err)
		return
	}

	// --- THIS IS THE PART THAT GOT DELETED ---
	// 3b. Open the local .mp4 file
	file, err := os.Open(localFilePath)
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Could not open local video file: %v\n", err)
		return
	}
	defer file.Close()
	// -----------------------------------------

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
		Fields("id, webContentLink"). // <--- NEW FIX: Request direct MP4 stream, not the HTML preview page
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
	videoLink := uploadedFile.WebContentLink // <--- NEW FIX: Map the direct streaming link
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

// RetryFailedUploads runs on server startup to find and upload stranded videos
func RetryFailedUploads() {
	log.Println("🔄 SECURITY: Scanning for stranded video uploads...")

	tempDir := filepath.Join("C:\\NinePOS_Data", "temp_security_vids")

	// 1. Recover Voided Transactions
	var strandedVoids []models.VoidedTransaction
	database.DB.Where("security_video_url LIKE ?", "PENDING_UPLOAD_%").Find(&strandedVoids)

	for _, voidTx := range strandedVoids {
		// Extract the SessionID from the "PENDING_UPLOAD_uuid" string
		sessionID := strings.Replace(voidTx.SecurityVideoURL, "PENDING_UPLOAD_", "", 1)
		filePath := filepath.Join(tempDir, fmt.Sprintf("session_%s.mp4", sessionID))

		// Check if the file actually exists locally
		if _, err := os.Stat(filePath); err == nil {
			log.Printf("♻️ SECURITY RECOVERY: Retrying upload for Voided Session %s\n", sessionID)
			go uploadSecurityVideo(sessionID, filePath, false, 0)
		}
	}

	// 2. Recover Successful Sales
	var strandedSales []models.Sale
	database.DB.Where("security_video_url LIKE ?", "PENDING_UPLOAD_%").Find(&strandedSales)

	for _, sale := range strandedSales {
		sessionID := strings.Replace(sale.SecurityVideoURL, "PENDING_UPLOAD_", "", 1)
		filePath := filepath.Join(tempDir, fmt.Sprintf("session_%s.mp4", sessionID))

		if _, err := os.Stat(filePath); err == nil {
			log.Printf("♻️ SECURITY RECOVERY: Retrying upload for Sale Order %d\n", sale.ID)
			go uploadSecurityVideo(sessionID, filePath, true, sale.ID)
		}
	}
}
