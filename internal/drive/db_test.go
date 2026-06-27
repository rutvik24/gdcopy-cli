package drive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDBOperations(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gdcopy_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")

	err = InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer CloseDB()

	// 1. Create upload session
	sessionID, err := CreateUploadSession("Test Session", "/local/path", "dest_folder_id")
	if err != nil {
		t.Fatalf("CreateUploadSession failed: %v", err)
	}

	// 2. Add folder
	folderID, err := AddFolderToSession(sessionID, "subfolder", "")
	if err != nil {
		t.Fatalf("AddFolderToSession failed: %v", err)
	}
	if folderID == 0 {
		t.Error("expected non-zero folder ID")
	}

	// 3. Add file
	fileID, err := AddFileToSession(sessionID, "/local/path/subfolder/file.txt", "subfolder/file.txt", "subfolder", 1024, "pending", "")
	if err != nil {
		t.Fatalf("AddFileToSession failed: %v", err)
	}
	if fileID == 0 {
		t.Error("expected non-zero file ID")
	}

	// 4. Retrieve incomplete sessions
	sessions, err := GetIncompleteSessions()
	if err != nil {
		t.Fatalf("GetIncompleteSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 incomplete session, got %d", len(sessions))
	} else if sessions[0].Name != "Test Session" {
		t.Errorf("expected session name 'Test Session', got '%s'", sessions[0].Name)
	}

	// 5. Retrieve folders and files
	folders, err := GetSessionFolders(sessionID)
	if err != nil {
		t.Fatalf("GetSessionFolders failed: %v", err)
	}
	if len(folders) != 1 || folders[0].RelativePath != "subfolder" {
		t.Errorf("folders retrieve incorrect: %v", folders)
	}

	files, err := GetSessionFiles(sessionID)
	if err != nil {
		t.Fatalf("GetSessionFiles failed: %v", err)
	}
	if len(files) != 1 || files[0].RelativePath != "subfolder/file.txt" {
		t.Errorf("files retrieve incorrect: %v", files)
	}

	// 6. Update folder Drive ID
	err = UpdateFolderDriveID(sessionID, "subfolder", "drive_folder_id_123")
	if err != nil {
		t.Fatalf("UpdateFolderDriveID failed: %v", err)
	}

	folders2, err := GetSessionFolders(sessionID)
	if err != nil || len(folders2) != 1 || folders2[0].DriveFolderID != "drive_folder_id_123" {
		t.Errorf("UpdateFolderDriveID did not persist: %v", folders2)
	}

	// 7. Update file status
	err = UpdateFileStatus(fileID, "uploaded", "drive_file_id_456", "")
	if err != nil {
		t.Fatalf("UpdateFileStatus failed: %v", err)
	}

	files2, err := GetSessionFiles(sessionID)
	if err != nil || len(files2) != 1 || files2[0].Status != "uploaded" || files2[0].DriveFileID != "drive_file_id_456" {
		t.Errorf("UpdateFileStatus did not persist: %v", files2)
	}

	// 8. Complete session
	err = UpdateSessionStatus(sessionID, "completed")
	if err != nil {
		t.Fatalf("UpdateSessionStatus failed: %v", err)
	}

	sessions2, err := GetIncompleteSessions()
	if err != nil {
		t.Fatalf("GetIncompleteSessions second retrieve failed: %v", err)
	}
	if len(sessions2) != 0 {
		t.Errorf("expected 0 incomplete sessions, got %d", len(sessions2))
	}
}

func TestOAuthTokenStorage(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "gdcopy_token_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer CloseDB()

	plaintext := []byte(`{"access_token":"test-token","token_type":"Bearer"}`)
	ciphertext, err := encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if err := SaveOAuthToken(ciphertext); err != nil {
		t.Fatalf("SaveOAuthToken failed: %v", err)
	}

	stored, err := LoadOAuthToken()
	if err != nil {
		t.Fatalf("LoadOAuthToken failed: %v", err)
	}
	if string(stored) != string(ciphertext) {
		t.Fatal("stored token does not match saved token")
	}

	// Upsert should overwrite
	ciphertext2, err := encrypt([]byte(`{"access_token":"refreshed-token","token_type":"Bearer"}`))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if err := SaveOAuthToken(ciphertext2); err != nil {
		t.Fatalf("SaveOAuthToken upsert failed: %v", err)
	}
	stored2, err := LoadOAuthToken()
	if err != nil {
		t.Fatalf("LoadOAuthToken after upsert failed: %v", err)
	}
	if string(stored2) != string(ciphertext2) {
		t.Fatal("upsert did not overwrite token")
	}

	if err := DeleteOAuthToken(); err != nil {
		t.Fatalf("DeleteOAuthToken failed: %v", err)
	}
	if _, err := LoadOAuthToken(); err == nil {
		t.Fatal("expected error loading token after delete")
	}
}
