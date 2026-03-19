// internal/handlers/security_handler.go
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
	"strconv" // <--- NEW FIX: Added to parse the Expense ID
	"strings"
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
	Stdin     io.WriteCloser // Pipe to send commands to FFmpeg
	FilePath  string         // Where the temporary video is saving
	IsClosing bool           // Flag for our 30-second overhang logic later
	Cutover   chan bool      // Channel to interrupt the 30s timer
}

var (
	// Map to securely track active recordings by SessionID
	activeRecordings = make(map[string]*ActiveRecording)
	// Mutex prevents server crashes if multiple cashiers trigger the camera at the exact same millisecond
	recordingMutex sync.Mutex
)

// StartRecording API - Triggered the moment the cart goes from 0 to 1 item
func StartRecording(c *gin.Context) {
	// --- The Instant Cutover Interceptor ---
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

	recordingMutex.Lock()
	defer recordingMutex.Unlock()

	// 1. Generate a unique ID for this cart session...
	sessionID := uuid.New().String()

	// 2. Define where the temporary video will be saved locally before cloud upload
	tempDir := filepath.Join("C:\\NinePOS_Data", "temp_security_vids")
	os.MkdirAll(tempDir, os.ModePerm) // Ensure folder exists
	fileName := fmt.Sprintf("session_%s.mp4", sessionID)
	filePath := filepath.Join(tempDir, fileName)

	// 3. Build the Universal FFmpeg Command (DXGI 30 FPS + Audio Capture)
	webcamName := os.Getenv("WEBCAM_DEVICE_NAME")
	if webcamName == "" {
		webcamName = "Logitech BRIO"
	}

	// NEW: Fetch the Microphone name from the environment file
	micName := os.Getenv("MIC_DEVICE_NAME")
	if micName == "" {
		micName = "Microphone (Logitech BRIO)" // Common Windows naming pattern
		log.Println("⚠️ SECURITY WARNING: MIC_DEVICE_NAME not found in .env, falling back to default.")
	}

	cameraInput := "video=" + webcamName
	audioInput := "audio=" + micName

	cmd := exec.Command("./ffmpeg.exe",
		"-y",
		"-thread_queue_size", "4096",
		// INPUT 0: Desktop Grab (Video)
		"-f", "lavfi", "-i", "ddagrab=framerate=30,hwdownload,format=bgra",
		"-thread_queue_size", "4096",

		// INPUT 1: Webcam Grab (Video) - THE FIX: Expanded Real-Time Buffer to 256MB
		"-f", "dshow", "-rtbufsize", "256M", "-video_size", "1280x720", "-framerate", "30", "-i", cameraInput,

		"-thread_queue_size", "4096",
		// INPUT 2: Microphone Grab (Audio)
		"-f", "dshow", "-i", audioInput,

		// THE LAYOUT FIX
		"-filter_complex", "[0:v]scale=1920:1080,fps=30[bg];[1:v]scale=640:-1,fps=30[cam];[bg][cam]overlay=W-w-10:H-h-10[outv]",

		// ROUTING
		"-map", "[outv]",
		"-map", "2:a",

		// SOFTWARE ENCODER
		"-vcodec", "libx264", "-preset", "ultrafast", "-crf", "28",
		"-acodec", "aac", "-b:a", "128k",
		"-r", "30",
		"-threads", "0",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		filePath,
	)

	// Print FFmpeg errors to the Go terminal for easy debugging
	cmd.Stderr = os.Stderr

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
		Cutover:   make(chan bool, 1),
	}

	log.Printf("🔴 SECURITY REC: Started recording session %s\n", sessionID)

	// 6. Return the SessionID to React
	c.JSON(http.StatusOK, gin.H{
		"message":    "Recording started successfully",
		"session_id": sessionID,
	})
}

// LogRemoval API - Silently logs when a cashier drops a single item to the trash can
func LogRemoval(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		ItemName  string `json:"item_name"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	logEntry := models.SuspiciousActivityLog{
		SessionID: req.SessionID,
		Action:    "PARTIAL_LINE_VOID",
		ItemName:  req.ItemName,
		Timestamp: time.Now(),
	}

	database.DB.Create(&logEntry)
	c.JSON(http.StatusOK, gin.H{"status": "partial_void_logged"})
}

// StopSuccess API - Triggered when the cashier successfully clicks "Pay & Print"
func StopSuccess(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		OrderID   uint   `json:"order_id"`
		ReceiptID string `json:"receipt_id"` // <-- NEW FIX: We now accept the Expense tag
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload"})
		return
	}

	// Hand off to the background Goroutine. We safely hijack the 'reason' parameter to pass the ReceiptID.
	go finalizeRecording(req.SessionID, true, req.OrderID, req.ReceiptID, 0, "")

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
func finalizeRecording(sessionID string, isSuccess bool, orderID uint, reasonOrReceipt string, valueLost float64, items string) {
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
		io.WriteString(session.Stdin, "q")
		session.Stdin.Close()

		recordingMutex.Unlock()
		session.Cmd.Wait()
		recordingMutex.Lock()
	} else if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
	}

	delete(activeRecordings, sessionID) // Clear from server memory
	recordingMutex.Unlock()

	// 4. Save to the correct database table based on whether it was a Success, Void, or Expense!
	uploadSessionID := sessionID

	if isSuccess {
		// --- NEW FIX: Extract the Expense ID if it exists ---
		if strings.HasPrefix(reasonOrReceipt, "EXPENSE-") {
			uploadSessionID = "expense_" + sessionID
			idStr := strings.TrimPrefix(reasonOrReceipt, "EXPENSE-")
			if parsedID, err := strconv.Atoi(idStr); err == nil {
				orderID = uint(parsedID)
			}
			database.DB.Model(&models.Expense{}).Where("id = ?", orderID).Update("security_video_url", "PENDING_UPLOAD_"+uploadSessionID)
		} else {
			// Normal Sale
			database.DB.Model(&models.Sale{}).Where("id = ?", orderID).Update("security_video_url", "PENDING_UPLOAD_"+uploadSessionID)
		}
	} else {
		// Create a brand new record in the VoidedTransactions table
		voidRecord := models.VoidedTransaction{
			SessionID:        sessionID,
			TotalValueLost:   valueLost,
			ItemsInCart:      items,
			Reason:           reasonOrReceipt, // Here it correctly logs the true Void Reason
			SecurityVideoURL: "PENDING_UPLOAD_" + sessionID,
			Timestamp:        time.Now(),
		}
		database.DB.Create(&voidRecord)
	}

	// 5. Trigger the Cloud Upload Process
	uploadSecurityVideo(uploadSessionID, session.FilePath, isSuccess, orderID)
}

// uploadSecurityVideo handles the Google Drive upload and database linking
func uploadSecurityVideo(sessionID string, localFilePath string, isSuccess bool, orderID uint) {
	log.Printf("☁️ SECURITY: Starting cloud upload for Session %s...\n", sessionID)

	folderID := os.Getenv("SECURITY_DRIVE_FOLDER_ID")
	if folderID == "" {
		log.Println("❌ SECURITY ERROR: SECURITY_DRIVE_FOLDER_ID is missing in .env")
		return
	}

	credPath := "credentials.json"
	if _, err := os.Stat("cmd/server/credentials.json"); err == nil {
		credPath = "cmd/server/credentials.json"
	}

	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile(credPath))
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Google Drive auth failed: %v\n", err)
		return
	}

	file, err := os.Open(localFilePath)
	if err != nil {
		log.Printf("❌ SECURITY ERROR: Could not open local video file: %v\n", err)
		return
	}
	defer file.Close()

	cloudFileName := filepath.Base(localFilePath)
	f := &drive.File{
		Name:    cloudFileName,
		Parents: []string{folderID},
	}

	uploadedFile, err := client.Files.Create(f).
		Media(file).
		SupportsAllDrives(true).
		Fields("id, webContentLink").
		Do()

	if err != nil {
		log.Printf("❌ SECURITY ERROR: Google Drive upload failed: %v\n", err)
		return
	}

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
	videoLink := uploadedFile.WebContentLink
	searchStr := "PENDING_UPLOAD_" + sessionID

	if isSuccess {
		if strings.HasPrefix(sessionID, "expense_") {
			// --- NEW FIX: Route to the Expense Table ---
			database.DB.Model(&models.Expense{}).Where("id = ?", orderID).Update("security_video_url", videoLink)
		} else {
			// Normal Sale
			database.DB.Model(&models.Sale{}).Where("id = ?", orderID).Update("security_video_url", videoLink)
		}
	} else if strings.HasPrefix(sessionID, "shift_open_") {
		database.DB.Model(&models.ShiftLog{}).Where("id = ?", orderID).Update("opening_video_url", videoLink)
	} else if strings.HasPrefix(sessionID, "shift_close_") {
		database.DB.Model(&models.ShiftLog{}).Where("id = ?", orderID).Update("closing_video_url", videoLink)
	} else {
		database.DB.Model(&models.VoidedTransaction{}).Where("security_video_url = ?", searchStr).Update("security_video_url", videoLink)
	}

	log.Printf("✅ SECURITY: Upload complete! Link saved to database. Deleting local file...\n")

	file.Close()
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
		sessionID := strings.Replace(voidTx.SecurityVideoURL, "PENDING_UPLOAD_", "", 1)
		filePath := filepath.Join(tempDir, fmt.Sprintf("session_%s.mp4", sessionID))

		if _, err := os.Stat(filePath); err == nil {
			log.Printf("♻️ SECURITY RECOVERY: Retrying upload for Voided Session %s\n", sessionID)
			go uploadSecurityVideo(sessionID, filePath, false, 0)
		}
	}

	// 2. Recover Successful Sales
	var strandedSales []models.Sale
	database.DB.Where("security_video_url LIKE ?", "PENDING_UPLOAD_%").Not("security_video_url LIKE ?", "PENDING_UPLOAD_expense_%").Find(&strandedSales)

	for _, sale := range strandedSales {
		sessionID := strings.Replace(sale.SecurityVideoURL, "PENDING_UPLOAD_", "", 1)
		filePath := filepath.Join(tempDir, fmt.Sprintf("session_%s.mp4", sessionID))

		if _, err := os.Stat(filePath); err == nil {
			log.Printf("♻️ SECURITY RECOVERY: Retrying upload for Sale Order %d\n", sale.ID)
			go uploadSecurityVideo(sessionID, filePath, true, sale.ID)
		}
	}

	// 3. Recover Till Payouts (Expenses) - NEW FIX
	var strandedExpenses []models.Expense
	database.DB.Where("security_video_url LIKE ?", "PENDING_UPLOAD_expense_%").Find(&strandedExpenses)

	for _, exp := range strandedExpenses {
		uploadSessionID := strings.Replace(exp.SecurityVideoURL, "PENDING_UPLOAD_", "", 1)
		originalSessionID := strings.TrimPrefix(uploadSessionID, "expense_")
		filePath := filepath.Join(tempDir, fmt.Sprintf("session_%s.mp4", originalSessionID))

		if _, err := os.Stat(filePath); err == nil {
			log.Printf("♻️ SECURITY RECOVERY: Retrying upload for Expense %d\n", exp.ID)
			go uploadSecurityVideo(uploadSessionID, filePath, true, exp.ID)
		}
	}
}
