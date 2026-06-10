package drive

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
