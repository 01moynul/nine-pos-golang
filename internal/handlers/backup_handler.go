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

func TriggerManualBackup(c *gin.Context) {
	err := processBackup("manual")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Manual backup completed successfully!"})
}

func GetBackupsList(c *gin.Context) {
	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth failed"})
		return
	}

	query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	// NEW: Added SupportsAllDrives and IncludeItemsFromAllDrives for Shared Workspaces
	fileList, err := client.Files.List().Q(query).
		SupportsAllDrives(true).
		IncludeItemsFromAllDrives(true).
		OrderBy("createdTime desc").
		Fields("files(id, name, createdTime, size)").Do()

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch files: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, fileList.Files)
}

func RunHourlyAutoBackup() {
	fmt.Println("🕰️ Running hourly auto-backup...")
	if err := processBackup("auto"); err != nil {
		fmt.Println("❌ Auto-backup failed:", err)
	} else {
		fmt.Println("✅ Auto-backup successful!")
	}
}

func CleanupOldAutoBackups() {
	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil || folderID == "" {
		return
	}

	sevenDaysAgo := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)
	query := fmt.Sprintf("'%s' in parents and name contains 'auto_' and modifiedTime < '%s'", folderID, sevenDaysAgo)

	fileList, err := client.Files.List().Q(query).
		SupportsAllDrives(true).
		IncludeItemsFromAllDrives(true).
		Fields("files(id, name)").Do()

	if err == nil {
		for _, file := range fileList.Files {
			client.Files.Delete(file.Id).SupportsAllDrives(true).Do()
			fmt.Printf("🗑️ Deleted old auto-backup: %s\n", file.Name)
		}
	}
}

// --- CORE LOGIC (Shared by Manual and Auto) ---
func processBackup(prefix string) error {
	// 1. Check Production Path
	dbPath := `C:\NinePOS_Data\inventory.db`
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Fallback to Development Path
		dbPath = "inventory.db"
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return fmt.Errorf("Database not found in C:\\NinePOS_Data or local root folder.")
		}
	}

	// 2. Create local backups temp folder
	backupDir := "backups"
	if err := os.MkdirAll(backupDir, os.ModePerm); err != nil {
		return fmt.Errorf("Failed to create backups folder: %v", err)
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	zipFileName := filepath.Join(backupDir, fmt.Sprintf("%s_backup_%s.zip", prefix, timestamp))

	// 3. Compress
	if err := createZip(zipFileName, dbPath); err != nil {
		os.Remove(zipFileName)
		return fmt.Errorf("Compression failed: %v", err)
	}

	// 4. Upload
	folderID := os.Getenv("GOOGLE_DRIVE_FOLDER_ID")
	if folderID == "" {
		os.Remove(zipFileName)
		return fmt.Errorf("GOOGLE_DRIVE_FOLDER_ID is missing")
	}

	if err := uploadToDrive(zipFileName, folderID); err != nil {
		os.Remove(zipFileName)
		return fmt.Errorf("Google Drive upload failed: %v", err)
	}

	os.Remove(zipFileName)
	return nil
}

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

func uploadToDrive(localFilePath string, folderID string) error {
	ctx := context.Background()
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
		Name:    filepath.Base(localFilePath), // Ensures cloud file name is clean
		Parents: []string{folderID},
	}

	// NEW: Added .SupportsAllDrives(true) to allow Shared Drive uploads!
	_, err = client.Files.Create(f).Media(file).SupportsAllDrives(true).Do()
	return err
}

// DeleteBackup removes a specific file from Google Drive
func DeleteBackup(c *gin.Context) {
	fileID := c.Param("id")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth failed"})
		return
	}

	err = client.Files.Delete(fileID).SupportsAllDrives(true).Do()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete file from Drive"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Backup deleted successfully"})
}

// DownloadBackup fetches the zip from Google Drive and sends it to the browser
func DownloadBackup(c *gin.Context) {
	fileID := c.Param("id")
	ctx := context.Background()
	client, err := drive.NewService(ctx, option.WithCredentialsFile("cmd/server/credentials.json"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth failed"})
		return
	}

	res, err := client.Files.Get(fileID).SupportsAllDrives(true).Download()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to download from Drive"})
		return
	}
	defer res.Body.Close()

	// Tell the browser this is a file download
	c.Header("Content-Disposition", "attachment; filename=ninepos_restore.zip")
	c.Header("Content-Type", "application/zip")
	io.Copy(c.Writer, res.Body)
}
