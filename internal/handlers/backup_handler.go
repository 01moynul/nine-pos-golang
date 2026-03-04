package handlers

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// TriggerManualBackup is called by the Admin from the React UI
func TriggerManualBackup(c *gin.Context) {
	err := processBackup("manual")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Manual backup completed successfully!"})
}

// GetBackupsList fetches all backups from Google Drive for the React UI
func GetBackupsList(c *gin.Context) {
	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json")) // Adjust path if needed
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth failed"})
		return
	}

	// Fetch files in the folder, ordered by newest first
	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	fileList, err := client.Files.List().Q(query).OrderBy("createdTime desc").Fields("files(id, name, createdTime, size)").Do()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch files"})
		return
	}

	c.JSON(http.StatusOK, fileList.Files)
}

// RunHourlyAutoBackup is triggered by the Go background ticker
func RunHourlyAutoBackup() {
	fmt.Println("🕰️ Running hourly auto-backup...")
	err := processBackup("auto")
	if err != nil {
		fmt.Println("❌ Auto-backup failed:", err)
	} else {
		fmt.Println("✅ Auto-backup successful!")
	}
}

// CleanupOldAutoBackups deletes "auto_" backups older than 7 days
func CleanupOldAutoBackups() {
	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil || folderID == "" {
		return
	}

	// Calculate date 7 days ago
	sevenDaysAgo := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)

	// Query: Find files named 'auto_*' older than 7 days
	query := fmt.Sprintf("'%s' in parents and name contains 'auto_' and modifiedTime < '%s'", folderID, sevenDaysAgo)

	fileList, err := client.Files.List().Q(query).Fields("files(id, name)").Do()
	if err == nil {
		for _, file := range fileList.Files {
			client.Files.Delete(file.Id).Do()
			fmt.Printf("🗑️ Deleted old auto-backup: %s\n", file.Name)
		}
	}
}

// --- CORE LOGIC (Shared by Manual and Auto) ---
func processBackup(prefix string) error {
	dbPath := "ninepos_local.db"
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	zipFileName := fmt.Sprintf("%s_backup_%s.zip", prefix, timestamp)

	if err := createZip(zipFileName, dbPath); err != nil {
		return fmt.Errorf("compress failed: %v", err)
	}

	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	if folderID == "" {
		os.Remove(zipFileName)
		return fmt.Errorf("GOOGLE_DRIVE_FOLDER_ID missing")
	}

	if err := uploadToDrive(zipFileName, folderID); err != nil {
		os.Remove(zipFileName)
		return fmt.Errorf("upload failed: %v", err)
	}

	os.Remove(zipFileName) // Clean up local zip
	return nil
}

// Helper function to zip a file
func createZip(zipName, fileName string) error {
	newZipFile, err := os.Create(zipName)
	if err != nil {
		return err
	}
	defer newZipFile.Close()

	zipWriter := zip.NewWriter(newZipFile)
	defer zipWriter.Close()

	fileToZip, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer fileToZip.Close()

	info, err := fileToZip.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.Base(fileName)
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, fileToZip)
	return err
}

// Helper function to handle Google Drive Upload
func uploadToDrive(localFilePath string, folderID string) error {
	ctx := context.Background()
	// IMPORTANT: Ensure this path matches where credentials.json is located relative to main.go
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil {
		return err
	}

	file, err := os.Open(localFilePath)
	if err != nil {
		return err
	}
	defer file.Close()

	f := &drive.File{
		Name:    localFilePath,
		Parents: []string{folderID},
	}

	_, err = client.Files.Create(f).Media(file).Do()
	return err
}
