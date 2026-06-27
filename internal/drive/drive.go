package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manifoldco/promptui"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// Folder represents a folder item in our local cache/navigation
type Folder struct {
	ID             string
	Name           string
	Parents        []string
	SharedWithMe   bool
}

// RenameResult represents the outcome of a renaming operation for a single file
type RenameResult struct {
	FileID  string
	OldName string
	NewName string
	Success bool
	Error   error
}

// FetchFolder retrieves a folder's metadata by ID
func FetchFolder(srv *drive.Service, id string) (*drive.File, error) {
	return executeWithRetry(func() (*drive.File, error) {
		return srv.Files.Get(id).Fields("id, name, parents").Do()
	})
}

// ListFoldersInParent retrieves all non-trashed subfolders within a specific parent folder ID
func ListFoldersInParent(srv *drive.Service, parentID string) ([]Folder, error) {
	query := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", parentID)
	
	var folders []Folder
	pageToken := ""

	for {
		call := srv.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, parents)").
			PageSize(100)
		
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		res, err := executeWithRetry(func() (*drive.FileList, error) {
			return call.Do()
		})
		if err != nil {
			return nil, fmt.Errorf("failed listing subfolders: %v", err)
		}

		for _, f := range res.Files {
			folders = append(folders, Folder{
				ID:           f.Id,
				Name:         f.Name,
				Parents:      f.Parents,
				SharedWithMe: false,
			})
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return folders, nil
}

// ListSharedWithMeFolders retrieves all folders explicitly shared with the user
func ListSharedWithMeFolders(srv *drive.Service) ([]Folder, error) {
	query := "sharedWithMe = true and mimeType = 'application/vnd.google-apps.folder' and trashed = false"
	
	var folders []Folder
	pageToken := ""

	for {
		call := srv.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, parents)").
			PageSize(100)
		
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		res, err := executeWithRetry(func() (*drive.FileList, error) {
			return call.Do()
		})
		if err != nil {
			return nil, fmt.Errorf("failed listing shared folders: %v", err)
		}

		for _, f := range res.Files {
			folders = append(folders, Folder{
				ID:           f.Id,
				Name:         f.Name,
				Parents:      f.Parents,
				SharedWithMe: true,
			})
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return folders, nil
}

// ListFilesAndFolders retrieves all files (excluding folders) in a given parent folder
func ListFilesAndFolders(srv *drive.Service, parentID string) ([]*drive.File, error) {
	query := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
	
	var files []*drive.File
	pageToken := ""

	for {
		call := srv.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, mimeType, size)").
			PageSize(100)
		
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		res, err := executeWithRetry(func() (*drive.FileList, error) {
			return call.Do()
		})
		if err != nil {
			return nil, fmt.Errorf("failed listing folder files: %v", err)
		}

		files = append(files, res.Files...)

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return files, nil
}

// CopyFolderRecursive copies a source folder recursively into a destination parent folder.
// It creates the folder hierarchy and copies all files concurrently.
func CopyFolderRecursive(srv *drive.Service, srcFolderID string, destParentFolderID string, newName string, collisionStrategy string, logFunc func(string)) (string, error) {
	// Limiting total concurrent file copies using a semaphore to avoid rate limits
	// Google Drive allows some concurrency, 10 is a good, safe default.
	sem := make(chan struct{}, 10)
	return copyFolderRecursiveInternal(srv, srcFolderID, destParentFolderID, newName, collisionStrategy, logFunc, sem)
}

// CopyFiles copies multiple files into a destination folder.
func CopyFiles(srv *drive.Service, files []*drive.File, destFolderID string, collisionStrategy string, logFunc func(string)) error {
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	var mapMu sync.Mutex

	// Retrieve all existing files in the destination folder to check for collisions
	existingFiles := make(map[string]string)

	if collisionStrategy != "" {
		destQuery := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed = false", destFolderID)
		destPageToken := ""

		for {
			call := srv.Files.List().
				Q(destQuery).
				Fields("nextPageToken, files(id, name)").
				PageSize(100)
			
			if destPageToken != "" {
				call = call.PageToken(destPageToken)
			}

			res, err := executeWithRetry(func() (*drive.FileList, error) {
				return call.Do()
			})
			if err != nil {
				return fmt.Errorf("failed to list existing files in destination folder: %v", err)
			}

			for _, f := range res.Files {
				existingFiles[f.Name] = f.Id
			}

			destPageToken = res.NextPageToken
			if destPageToken == "" {
				break
			}
		}
	}

	for _, file := range files {
		wg.Add(1)
		go func(file *drive.File) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			var targetName string
			var existingID string
			var shouldSkip bool

			mapMu.Lock()
			if id, exists := existingFiles[file.Name]; exists && collisionStrategy != "" {
				existingID = id
				if collisionStrategy == "skip" {
					shouldSkip = true
				} else if collisionStrategy == "suffix" {
					ext := filepath.Ext(file.Name)
					base := strings.TrimSuffix(file.Name, ext)
					counter := 1
					for {
						candidate := fmt.Sprintf("%s (%d)%s", base, counter, ext)
						if _, exists2 := existingFiles[candidate]; !exists2 {
							targetName = candidate
							break
						}
						counter++
					}
					existingFiles[targetName] = "reserved"
				} else {
					// overwrite
					targetName = file.Name
				}
			} else {
				targetName = file.Name
				if collisionStrategy != "" {
					existingFiles[targetName] = "reserved"
				}
			}
			mapMu.Unlock()

			if shouldSkip {
				logFunc(fmt.Sprintf("File '%s' already exists in destination. Skipping copy.", file.Name))
				return
			}

			if existingID != "" && collisionStrategy == "overwrite" {
				logFunc(fmt.Sprintf("File '%s' already exists in destination. Overwriting...", file.Name))
				_, err := executeWithRetry(func() (interface{}, error) {
					err := srv.Files.Delete(existingID).Do()
					return nil, err
				})
				if err != nil {
					logFunc(fmt.Errorf("error deleting existing file '%s': %v", file.Name, err).Error())
				}
			}

			if targetName == "" {
				targetName = file.Name
			}

			logFunc(fmt.Sprintf("Copying file: %s ...", file.Name))
			copyRef := &drive.File{
				Name:    targetName,
				Parents: []string{destFolderID},
			}
			_, err := executeWithRetry(func() (*drive.File, error) {
				return srv.Files.Copy(file.Id, copyRef).Do()
			})
			if err != nil {
				logFunc(fmt.Errorf("error copying file '%s': %v", file.Name, err).Error())
			} else {
				if targetName != file.Name {
					logFunc(fmt.Sprintf("Finished copying: %s (copied as %s)", file.Name, targetName))
				} else {
					logFunc(fmt.Sprintf("Finished copying: %s", file.Name))
				}
			}
		}(file)
	}

	wg.Wait()
	return nil
}

// CreateFolder creates a new folder with the given name inside the parent folder.
func CreateFolder(srv *drive.Service, name string, parentID string) (*drive.File, error) {
	destFolderMeta := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}

	return executeWithRetry(func() (*drive.File, error) {
		return srv.Files.Create(destFolderMeta).Fields("id").Do()
	})
}

func copyFolderRecursiveInternal(srv *drive.Service, srcFolderID string, destParentFolderID string, newName string, collisionStrategy string, logFunc func(string), sem chan struct{}) (string, error) {
	// 1. Get Source Folder Details if name not provided
	srcFolder, err := executeWithRetry(func() (*drive.File, error) {
		return srv.Files.Get(srcFolderID).Fields("id, name").Do()
	})
	if err != nil {
		return "", fmt.Errorf("failed to fetch source folder details: %v", err)
	}

	folderName := srcFolder.Name
	if newName != "" {
		folderName = newName
	}

	// Check if a folder with this name already exists in destParentFolderID to support merging
	var newFolderID string
	var folderExists bool

	checkQuery := fmt.Sprintf("'%s' in parents and name = '%s' and mimeType = 'application/vnd.google-apps.folder' and trashed = false", destParentFolderID, strings.ReplaceAll(folderName, "'", "\\'"))
	existingFoldersList, err := executeWithRetry(func() (*drive.FileList, error) {
		return srv.Files.List().Q(checkQuery).Fields("files(id, name)").Do()
	})
	if err == nil && len(existingFoldersList.Files) > 0 {
		newFolderID = existingFoldersList.Files[0].Id
		folderExists = true
		logFunc(fmt.Sprintf("Folder '%s' already exists in destination. Merging contents...", folderName))
	}

	if !folderExists {
		logFunc(fmt.Sprintf("Creating folder '%s' in destination...", folderName))

		// 2. Create directory in destination
		destFolderMeta := &drive.File{
			Name:     folderName,
			MimeType: "application/vnd.google-apps.folder",
			Parents:  []string{destParentFolderID},
		}
		destFolder, err := executeWithRetry(func() (*drive.File, error) {
			return srv.Files.Create(destFolderMeta).Fields("id").Do()
		})
		if err != nil {
			return "", fmt.Errorf("failed to create folder in destination: %v", err)
		}
		newFolderID = destFolder.Id
	}

	// Retrieve all existing files in the destination folder to check for collisions
	existingFiles := make(map[string]string)
	var mapMu sync.Mutex

	if collisionStrategy != "" {
		destQuery := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed = false", newFolderID)
		destPageToken := ""

		for {
			call := srv.Files.List().
				Q(destQuery).
				Fields("nextPageToken, files(id, name)").
				PageSize(100)
			
			if destPageToken != "" {
				call = call.PageToken(destPageToken)
			}

			res, err := executeWithRetry(func() (*drive.FileList, error) {
				return call.Do()
			})
			if err != nil {
				return "", fmt.Errorf("failed to list existing files in destination folder: %v", err)
			}

			for _, f := range res.Files {
				existingFiles[f.Name] = f.Id
			}

			destPageToken = res.NextPageToken
			if destPageToken == "" {
				break
			}
		}
	}

	// 3. List all files and subfolders in the source folder
	query := fmt.Sprintf("'%s' in parents and trashed = false", srcFolderID)
	pageToken := ""

	for {
		call := srv.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, mimeType)").
			PageSize(100)
		
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		res, err := executeWithRetry(func() (*drive.FileList, error) {
			return call.Do()
		})
		if err != nil {
			return "", fmt.Errorf("failed to list items in source folder: %v", err)
		}

		var wg sync.WaitGroup
		var subfolders []*drive.File

		for _, item := range res.Files {
			if item.MimeType == "application/vnd.google-apps.folder" {
				subfolders = append(subfolders, item)
			} else {
				// Copy file concurrently
				wg.Add(1)
				go func(file *drive.File) {
					defer wg.Done()

					sem <- struct{}{}
					defer func() { <-sem }()

					var targetName string
					var existingID string
					var shouldSkip bool

					mapMu.Lock()
					if id, exists := existingFiles[file.Name]; exists && collisionStrategy != "" {
						existingID = id
						if collisionStrategy == "skip" {
							shouldSkip = true
						} else if collisionStrategy == "suffix" {
							ext := filepath.Ext(file.Name)
							base := strings.TrimSuffix(file.Name, ext)
							counter := 1
							for {
								candidate := fmt.Sprintf("%s (%d)%s", base, counter, ext)
								if _, exists2 := existingFiles[candidate]; !exists2 {
									targetName = candidate
									break
								}
								counter++
							}
							existingFiles[targetName] = "reserved"
						} else {
							// overwrite
							targetName = file.Name
						}
					} else {
						targetName = file.Name
						if collisionStrategy != "" {
							existingFiles[targetName] = "reserved"
						}
					}
					mapMu.Unlock()

					if shouldSkip {
						logFunc(fmt.Sprintf("File '%s' already exists in destination. Skipping copy.", file.Name))
						return
					}

					if existingID != "" && collisionStrategy == "overwrite" {
						logFunc(fmt.Sprintf("File '%s' already exists in destination. Overwriting...", file.Name))
						_, err := executeWithRetry(func() (interface{}, error) {
							err := srv.Files.Delete(existingID).Do()
							return nil, err
						})
						if err != nil {
							logFunc(fmt.Errorf("error deleting existing file '%s': %v", file.Name, err).Error())
						}
					}

					if targetName == "" {
						targetName = file.Name
					}

					logFunc(fmt.Sprintf("Copying file: %s ...", file.Name))
					copyRef := &drive.File{
						Name:    targetName,
						Parents: []string{newFolderID},
					}
					_, err := executeWithRetry(func() (*drive.File, error) {
						return srv.Files.Copy(file.Id, copyRef).Do()
					})
					if err != nil {
						logFunc(fmt.Errorf("error copying file '%s': %v", file.Name, err).Error())
					} else {
						if targetName != file.Name {
							logFunc(fmt.Sprintf("Finished copying: %s (copied as %s)", file.Name, targetName))
						} else {
							logFunc(fmt.Sprintf("Finished copying: %s", file.Name))
						}
					}
				}(item)
			}
		}

		// Wait for files in this page to finish copying
		wg.Wait()

		// Recursively copy subfolders
		for _, subfolder := range subfolders {
			_, err = copyFolderRecursiveInternal(srv, subfolder.Id, newFolderID, "", collisionStrategy, logFunc, sem)
			if err != nil {
				logFunc(fmt.Errorf("error copying subfolder '%s': %v", subfolder.Name, err).Error())
			}
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	logFunc(fmt.Sprintf("Successfully copied folder contents to '%s'", folderName))
	return newFolderID, nil
}

// RenameFiles performs bulk renaming of the provided files according to the strategy
func RenameFiles(srv *drive.Service, files []*drive.File, strategy string, param1 string, param2 string, dryRun bool) []RenameResult {
	var results []RenameResult

	for i, f := range files {
		// Skip folders for file renaming
		if f.MimeType == "application/vnd.google-apps.folder" {
			continue
		}

		oldName := f.Name
		newName := oldName

		switch strategy {
		case "prefix":
			newName = param1 + oldName
		case "suffix":
			ext := filepath.Ext(oldName)
			base := strings.TrimSuffix(oldName, ext)
			newName = base + param1 + ext
		case "replace":
			// param1: find, param2: replace
			newName = strings.ReplaceAll(oldName, param1, param2)
		case "number":
			// param1: base name, param2: start number (format base_01.ext, base_02.ext)
			ext := filepath.Ext(oldName)
			// Calculate padding based on total files count, minimum 2 digits
			padLength := 2
			if len(files) >= 100 {
				padLength = 3
			}
			if len(files) >= 1000 {
				padLength = 4
			}
			
			// Start index
			var startIdx int
			_, _ = fmt.Sscanf(param2, "%d", &startIdx)
			idx := startIdx + i
			
			formatStr := fmt.Sprintf("%%s_%%0%dd%%s", padLength)
			newName = fmt.Sprintf(formatStr, param1, idx, ext)
		}

		if newName == oldName {
			// No change needed
			continue
		}

		res := RenameResult{
			FileID:  f.Id,
			OldName: oldName,
			NewName: newName,
		}

		if !dryRun {
			updateMeta := &drive.File{
				Name: newName,
			}
			_, err := executeWithRetry(func() (*drive.File, error) {
				return srv.Files.Update(f.Id, updateMeta).Do()
			})
			if err != nil {
				res.Success = false
				res.Error = err
			} else {
				res.Success = true
			}
		} else {
			res.Success = true // Dry-run success simulation
		}

		results = append(results, res)
	}

	return results
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	if gerr, ok := err.(*googleapi.Error); ok {
		if gerr.Code == 429 || gerr.Code == 403 || (gerr.Code >= 500 && gerr.Code < 600) {
			return true
		}
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "quota exceeded") || strings.Contains(errStr, "user limit exceeded") {
		return true
	}
	return false
}

func executeWithRetry[T any](operation func() (T, error)) (T, error) {
	var result T
	var err error
	backoff := 1 * time.Second
	maxBackoff := 16 * time.Second
	maxRetries := 5

	for i := 0; i <= maxRetries; i++ {
		result, err = operation()
		if err == nil {
			return result, nil
		}

		if isRetriableError(err) && i < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		break
	}
	return result, err
}

// UploadFileToDrive uploads a local file to a Google Drive folder.
func UploadFileToDrive(ctx context.Context, srv *drive.Service, localFilePath string, driveFileName string, parentFolderID string) (*drive.File, error) {
	driveFile := &drive.File{
		Name:    driveFileName,
		Parents: []string{parentFolderID},
	}

	return executeWithRetry(func() (*drive.File, error) {
		f, err := os.Open(localFilePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		return srv.Files.Create(driveFile).Media(f).Context(ctx).Do()
	})
}

// StartNewUploadSession initializes a new upload session, creates DB records, and returns the session ID.
func StartNewUploadSession(srv *drive.Service, name string, localPaths []string, destFolderID string) (int64, error) {
	pathsJSON, err := json.Marshal(localPaths)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal local paths: %v", err)
	}

	sessionID, err := CreateUploadSession(name, string(pathsJSON), destFolderID)
	if err != nil {
		return 0, fmt.Errorf("failed to create upload session in DB: %v", err)
	}

	for _, localPath := range localPaths {
		fi, err := os.Stat(localPath)
		if err != nil {
			_ = UpdateSessionStatus(sessionID, "failed")
			return 0, fmt.Errorf("local path error: %v", err)
		}

		if fi.IsDir() {
			parentDir := filepath.Dir(localPath)
			err = filepath.Walk(localPath, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}

				relPath, err := filepath.Rel(parentDir, path)
				if err != nil {
					return err
				}

				if info.IsDir() {
					driveFolderID, found := FindCreatedFolder(destFolderID, relPath)
					if !found {
						driveFolderID = ""
					}
					_, err = AddFolderToSession(sessionID, relPath, driveFolderID)
					return err
				} else {
					parentRelPath := filepath.Dir(relPath)
					if parentRelPath == "." {
						parentRelPath = ""
					}
					driveFileID, found := FindUploadedFile(destFolderID, relPath, info.Size())
					status := "pending"
					if found {
						status = "uploaded"
					} else {
						driveFileID = ""
					}
					_, err = AddFileToSession(sessionID, path, relPath, parentRelPath, info.Size(), status, driveFileID)
					return err
				}
			})
			if err != nil {
				_ = UpdateSessionStatus(sessionID, "failed")
				return 0, fmt.Errorf("failed walking local folder %s: %v", localPath, err)
			}
		} else {
			// Single file upload
			relPath := filepath.Base(localPath)
			driveFileID, found := FindUploadedFile(destFolderID, relPath, fi.Size())
			status := "pending"
			if found {
				status = "uploaded"
			} else {
				driveFileID = ""
			}
			_, err = AddFileToSession(sessionID, localPath, relPath, "", fi.Size(), status, driveFileID)
			if err != nil {
				_ = UpdateSessionStatus(sessionID, "failed")
				return 0, fmt.Errorf("failed to register file in session DB: %v", err)
			}
		}
	}

	return sessionID, nil
}

// RunUploadSession runs/resumes an upload session.
func RunUploadSession(ctx context.Context, srv *drive.Service, sessionID int64, logFunc func(string)) error {
	// 1. Get session info
	var session UploadSession
	err := DB.QueryRow("SELECT id, name, local_path, dest_folder_id, status FROM upload_sessions WHERE id = ?", sessionID).
		Scan(&session.ID, &session.Name, &session.LocalPath, &session.DestFolderID, &session.Status)
	if err != nil {
		return fmt.Errorf("failed to fetch session: %v", err)
	}

	if session.Status == "completed" {
		logFunc("Session already completed.")
		return nil
	}

	var driveFolderMap = make(map[string]string) // relative_path -> drive_folder_id

	// 2. Load folders and create missing ones
	folders, err := GetSessionFolders(sessionID)
	if err != nil {
		return fmt.Errorf("failed to fetch session folders: %v", err)
	}

	if len(folders) > 0 {
		// Sort folders by depth of relative_path (root "" first)
		sort.Slice(folders, func(i, j int) bool {
			cI := strings.Count(folders[i].RelativePath, "/")
			cJ := strings.Count(folders[j].RelativePath, "/")
			if folders[i].RelativePath == "" {
				return true
			}
			if folders[j].RelativePath == "" {
				return false
			}
			return cI < cJ
		})

		// Create folders on Google Drive
		for i, f := range folders {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if f.DriveFolderID != "" {
				driveFolderMap[f.RelativePath] = f.DriveFolderID
				continue
			}

			var folderName string
			var parentDriveID string

			if f.RelativePath == "" {
				var paths []string
				if json.Unmarshal([]byte(session.LocalPath), &paths) == nil && len(paths) > 0 {
					folderName = filepath.Base(paths[0])
				} else {
					folderName = filepath.Base(session.LocalPath)
				}
				parentDriveID = session.DestFolderID
			} else {
				folderName = filepath.Base(f.RelativePath)
				parentRelPath := filepath.Dir(f.RelativePath)
				if parentRelPath == "." {
					parentRelPath = ""
				}
				if parentRelPath == "" {
					parentDriveID = session.DestFolderID
				} else {
					parentDriveID = driveFolderMap[parentRelPath]
					if parentDriveID == "" {
						return fmt.Errorf("parent folder Drive ID not found for: %s", f.RelativePath)
					}
				}
			}

			logFunc(fmt.Sprintf("Creating folder on Drive: %s ...", f.RelativePath))
			newFolder, err := CreateFolder(srv, folderName, parentDriveID)
			if err != nil {
				return fmt.Errorf("failed to create folder '%s' on Drive: %v", f.RelativePath, err)
			}

			err = UpdateFolderDriveID(sessionID, f.RelativePath, newFolder.Id)
			if err != nil {
				return fmt.Errorf("failed to update folder in DB: %v", err)
			}

			folders[i].DriveFolderID = newFolder.Id
			driveFolderMap[f.RelativePath] = newFolder.Id
		}
	}

	// 3. Load files and upload pending ones
	files, err := GetSessionFiles(sessionID)
	if err != nil {
		return fmt.Errorf("failed to fetch session files: %v", err)
	}

	var pendingFiles []UploadFile
	for _, f := range files {
		if f.Status != "uploaded" {
			pendingFiles = append(pendingFiles, f)
		}
	}

	if len(pendingFiles) == 0 {
		// All files uploaded, mark session as completed
		err = UpdateSessionStatus(sessionID, "completed")
		if err != nil {
			return fmt.Errorf("failed to mark session completed: %v", err)
		}
		logFunc("All files successfully uploaded.")
		// Recheck if all files are uploaded now
		reloadedFiles, err := GetSessionFiles(sessionID)
		if err == nil {
			allDone := true
			for _, rf := range reloadedFiles {
				if rf.Status != "uploaded" {
					allDone = false
					break
				}
			}
			if allDone {
				_ = UpdateSessionStatus(sessionID, "completed")
				logFunc("All files successfully uploaded.")
				return nil
			}
		}
	}
	logFunc(fmt.Sprintf("Found %d pending file(s) to upload.", len(pendingFiles)))

	// Cache of files existing in destination folders on Drive: parentID -> name -> File info
	destFilesCache := make(map[string]map[string]*drive.File)
	var verifiedPendingFiles []UploadFile

	for _, fRec := range pendingFiles {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Determine target parent folder ID
		var targetParentID string
		if fRec.DestFolderRelativePath != "" {
			targetParentID = driveFolderMap[fRec.DestFolderRelativePath]
		} else {
			targetParentID = session.DestFolderID
		}

		if targetParentID == "" {
			// Parent folder not created or not found, keep in queue (will fail during upload)
			verifiedPendingFiles = append(verifiedPendingFiles, fRec)
			continue
		}

		// Load cache for this parent folder if not cached yet
		if destFilesCache[targetParentID] == nil {
			logFunc(fmt.Sprintf("Scanning destination folder (ID: %s) for existing files...", targetParentID))
			driveFiles, err := ListFilesAndFolders(srv, targetParentID)
			if err != nil {
				logFunc(fmt.Sprintf("Warning: failed to scan destination folder: %v", err))
				destFilesCache[targetParentID] = make(map[string]*drive.File)
			} else {
				cache := make(map[string]*drive.File)
				for _, df := range driveFiles {
					if df.MimeType != "application/vnd.google-apps.folder" {
						cache[df.Name] = df
					}
				}
				destFilesCache[targetParentID] = cache
			}
		}

		fileName := filepath.Base(fRec.LocalPath)
		if df, exists := destFilesCache[targetParentID][fileName]; exists {
			// File exists in Drive! Compare sizes
			if df.Size == fRec.Size {
				logFunc(fmt.Sprintf("File '%s' already exists on Drive with matching size (%d B). Skipping upload and updating database.", fRec.RelativePath, fRec.Size))
				err = UpdateFileStatus(fRec.ID, "uploaded", df.Id, "")
				if err != nil {
					logFunc(fmt.Sprintf("Failed to update status in DB for %s: %v", fRec.RelativePath, err))
				}
				continue
			} else {
				// Size mismatch! Prompt the user
				logFunc(fmt.Sprintf("\n[Collision] File '%s' already exists on Drive but size differs!", fRec.RelativePath))
				logFunc(fmt.Sprintf("  Local Size: %d B", fRec.Size))
				logFunc(fmt.Sprintf("  Drive Size: %d B", df.Size))

				prompt := promptui.Select{
					Label: fmt.Sprintf("Choose action for '%s'", fileName),
					Items: []string{
						"Replace (Delete the file on Drive and upload the local one)",
						"Keep both (Upload the local file anyway, keeping the existing one)",
						"Skip (Do not upload the local file, mark as uploaded in DB)",
					},
				}

				idx, _, err := prompt.Run()
				if err != nil {
					if err == promptui.ErrInterrupt {
						return fmt.Errorf("interrupted by user")
					}
					logFunc("Prompt error. Skipping file.")
					continue
				}

				switch idx {
				case 0:
					// Replace: Delete existing file
					logFunc(fmt.Sprintf("Deleting existing file '%s' (ID: %s) on Drive...", fileName, df.Id))
					_, delErr := executeWithRetry(func() (interface{}, error) {
						err := srv.Files.Delete(df.Id).Context(ctx).Do()
						return nil, err
					})
					if delErr != nil {
						logFunc(fmt.Sprintf("Error deleting existing file: %v. Proceeding to upload anyway...", delErr))
					} else {
						// Remove from cache
						delete(destFilesCache[targetParentID], fileName)
					}
					verifiedPendingFiles = append(verifiedPendingFiles, fRec)
				case 1:
					// Keep both: proceed to upload
					verifiedPendingFiles = append(verifiedPendingFiles, fRec)
				case 2:
					// Skip: update status in DB
					logFunc(fmt.Sprintf("Skipping upload of '%s'. Updating database status.", fRec.RelativePath))
					err = UpdateFileStatus(fRec.ID, "uploaded", df.Id, "")
					if err != nil {
						logFunc(fmt.Sprintf("Failed to update status in DB for %s: %v", fRec.RelativePath, err))
					}
				}
			}
		} else {
			// File does not exist, upload it
			verifiedPendingFiles = append(verifiedPendingFiles, fRec)
		}
	}

	pendingFiles = verifiedPendingFiles

	if len(pendingFiles) == 0 {
		// Recheck if all files are uploaded now
		reloadedFiles, err := GetSessionFiles(sessionID)
		if err == nil {
			allDone := true
			for _, rf := range reloadedFiles {
				if rf.Status != "uploaded" {
					allDone = false
					break
				}
			}
			if allDone {
				_ = UpdateSessionStatus(sessionID, "completed")
				logFunc("All files successfully uploaded.")
				return nil
			}
		}
	}

	// Upload files concurrently using a semaphore to control concurrency
	concurrency := 4
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var uploadErrMu sync.Mutex
	var firstErr error

	for _, fileRec := range pendingFiles {
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(fRec UploadFile) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			// If context is already cancelled, abort
			if ctx.Err() != nil {
				return
			}

			// Determine target parent folder ID
			var targetParentID string
			if fRec.DestFolderRelativePath != "" {
				targetParentID = driveFolderMap[fRec.DestFolderRelativePath]
				if targetParentID == "" {
					errStr := fmt.Sprintf("target parent Drive ID not found for file %s", fRec.RelativePath)
					logFunc("Error: " + errStr)
					_ = UpdateFileStatus(fRec.ID, "failed", "", errStr)
					uploadErrMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("%s", errStr)
					}
					uploadErrMu.Unlock()
					return
				}
			} else {
				targetParentID = session.DestFolderID
			}

			fileName := filepath.Base(fRec.LocalPath)
			logFunc(fmt.Sprintf("Uploading file: %s ...", fRec.RelativePath))

			dbUpdated := false
			var driveFileID string

			// Defer block to delete the file from Google Drive if it succeeded but failed to save in DB due to cancellation or error
			defer func() {
				if driveFileID != "" && !dbUpdated {
					logFunc(fmt.Sprintf("Cleanup: Deleting uploaded file %s (ID: %s) due to interruption...", fRec.RelativePath, driveFileID))
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cleanupCancel()
					_, deleteErr := executeWithRetry(func() (interface{}, error) {
						err := srv.Files.Delete(driveFileID).Context(cleanupCtx).Do()
						return nil, err
					})
					if deleteErr != nil {
						logFunc(fmt.Sprintf("Cleanup Error: Failed to delete file %s from Drive: %v", fRec.RelativePath, deleteErr))
					}
				}
			}()

			res, err := UploadFileToDrive(ctx, srv, fRec.LocalPath, fileName, targetParentID)
			if err != nil {
				errMsg := err.Error()
				logFunc(fmt.Sprintf("Failed uploading file %s: %v", fRec.RelativePath, err))
				if ctx.Err() == nil {
					_ = UpdateFileStatus(fRec.ID, "failed", "", errMsg)
				}

				uploadErrMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				uploadErrMu.Unlock()
				return
			}

			driveFileID = res.Id

			if ctx.Err() != nil {
				return
			}

			// Successfully uploaded! Update status in DB
			err = UpdateFileStatus(fRec.ID, "uploaded", driveFileID, "")
			if err != nil {
				logFunc(fmt.Sprintf("Failed to update status in DB for %s: %v", fRec.RelativePath, err))
			} else {
				dbUpdated = true
				logFunc(fmt.Sprintf("Finished uploading: %s", fRec.RelativePath))
			}
		}(fileRec)
	}

	wg.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Recheck if all files are uploaded now
	reloadedFiles, err := GetSessionFiles(sessionID)
	if err != nil {
		return fmt.Errorf("failed to re-check session files: %v", err)
	}

	allDone := true
	for _, rf := range reloadedFiles {
		if rf.Status != "uploaded" {
			allDone = false
			break
		}
	}

	if allDone {
		err = UpdateSessionStatus(sessionID, "completed")
		if err != nil {
			return fmt.Errorf("failed to finalize session status: %v", err)
		}
		logFunc("Upload session completed successfully!")
	} else {
		logFunc("Upload session interrupted. Some files failed to upload.")
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("some files failed to upload")
	}

	return nil
}

