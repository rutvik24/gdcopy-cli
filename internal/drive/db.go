package drive

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Global DB instance
var DB *sql.DB

// UploadSession represents a record in upload_sessions table
type UploadSession struct {
	ID           int64
	Name         string
	LocalPath    string
	DestFolderID string
	Status       string // 'incomplete', 'completed'
	CreatedAt    time.Time
}

// UploadFolder represents a record in upload_folders table
type UploadFolder struct {
	ID            int64
	SessionID     int64
	RelativePath  string
	DriveFolderID string
}

// UploadFile represents a record in upload_files table
type UploadFile struct {
	ID                     int64
	SessionID              int64
	LocalPath              string
	RelativePath           string
	DestFolderRelativePath string
	Size                   int64
	Status                 string // 'pending', 'uploaded', 'failed'
	DriveFileID            string
	ErrorMsg               string
	UpdatedAt              time.Time
}

// InitDB initializes the SQLite database and creates tables if they don't exist
func InitDB(dbPath string) error {
	var err error
	DB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open sqlite database: %v", err)
	}

	// Set max open connections to 1 to serialize writes and avoid database locked errors
	DB.SetMaxOpenConns(1)

	// Enable foreign keys
	_, err = DB.Exec("PRAGMA foreign_keys = ON;")
	if err != nil {
		return fmt.Errorf("failed to enable foreign keys: %v", err)
	}

	// Create tables
	err = createTables()
	if err != nil {
		return fmt.Errorf("failed to create tables: %v", err)
	}

	return nil
}

// CloseDB closes the database connection
func CloseDB() {
	if DB != nil {
		DB.Close()
	}
}

func createTables() error {
	sessionsTable := `
	CREATE TABLE IF NOT EXISTS upload_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		local_path TEXT NOT NULL,
		dest_folder_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'incomplete',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`

	foldersTable := `
	CREATE TABLE IF NOT EXISTS upload_folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		relative_path TEXT NOT NULL,
		drive_folder_id TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (session_id) REFERENCES upload_sessions(id) ON DELETE CASCADE
	);`

	filesTable := `
	CREATE TABLE IF NOT EXISTS upload_files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		local_path TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		dest_folder_relative_path TEXT NOT NULL,
		size INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		drive_file_id TEXT NOT NULL DEFAULT '',
		error_msg TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES upload_sessions(id) ON DELETE CASCADE
	);`

	if _, err := DB.Exec(sessionsTable); err != nil {
		return err
	}
	if _, err := DB.Exec(foldersTable); err != nil {
		return err
	}
	if _, err := DB.Exec(filesTable); err != nil {
		return err
	}

	oauthTokensTable := `
	CREATE TABLE IF NOT EXISTS oauth_tokens (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		encrypted_token BLOB NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := DB.Exec(oauthTokensTable); err != nil {
		return err
	}

	return nil
}

// SaveOAuthToken stores an encrypted OAuth token in the database.
func SaveOAuthToken(encryptedToken []byte) error {
	query := `
		INSERT INTO oauth_tokens (id, encrypted_token, updated_at)
		VALUES (1, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			encrypted_token = excluded.encrypted_token,
			updated_at = CURRENT_TIMESTAMP`
	_, err := DB.Exec(query, encryptedToken)
	return err
}

// LoadOAuthToken retrieves the encrypted OAuth token from the database.
func LoadOAuthToken() ([]byte, error) {
	var encryptedToken []byte
	err := DB.QueryRow(`SELECT encrypted_token FROM oauth_tokens WHERE id = 1`).Scan(&encryptedToken)
	if err != nil {
		return nil, err
	}
	return encryptedToken, nil
}

// DeleteOAuthToken removes the stored OAuth token from the database.
func DeleteOAuthToken() error {
	_, err := DB.Exec(`DELETE FROM oauth_tokens WHERE id = 1`)
	return err
}

// CreateUploadSession inserts a new upload session
func CreateUploadSession(name, localPath, destFolderID string) (int64, error) {
	query := `INSERT INTO upload_sessions (name, local_path, dest_folder_id, status) VALUES (?, ?, ?, 'incomplete')`
	res, err := DB.Exec(query, name, localPath, destFolderID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddFolderToSession inserts a folder associated with a session, optionally with a pre-existing drive folder ID
func AddFolderToSession(sessionID int64, relativePath, driveFolderID string) (int64, error) {
	query := `INSERT INTO upload_folders (session_id, relative_path, drive_folder_id) VALUES (?, ?, ?)`
	res, err := DB.Exec(query, sessionID, relativePath, driveFolderID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddFileToSession inserts a file associated with a session, optionally with pre-existing status and drive file ID
func AddFileToSession(sessionID int64, localPath, relativePath, destFolderRelPath string, size int64, status, driveFileID string) (int64, error) {
	query := `INSERT INTO upload_files (session_id, local_path, relative_path, dest_folder_relative_path, size, status, drive_file_id) VALUES (?, ?, ?, ?, ?, ?, ?)`
	res, err := DB.Exec(query, sessionID, localPath, relativePath, destFolderRelPath, size, status, driveFileID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FindUploadedFile searches for a previously uploaded file in another session with the same relative path, size, and destination folder
func FindUploadedFile(destFolderID, relativePath string, size int64) (string, bool) {
	query := `
		SELECT f.drive_file_id 
		FROM upload_files f 
		JOIN upload_sessions s ON f.session_id = s.id 
		WHERE s.dest_folder_id = ? 
		  AND f.relative_path = ? 
		  AND f.status = 'uploaded' 
		  AND f.drive_file_id != ''
		  AND f.size = ?
		ORDER BY s.created_at DESC
		LIMIT 1`
	var driveFileID string
	err := DB.QueryRow(query, destFolderID, relativePath, size).Scan(&driveFileID)
	if err != nil {
		return "", false
	}
	return driveFileID, true
}

// FindCreatedFolder searches for a previously created folder in another session with the same relative path and destination folder
func FindCreatedFolder(destFolderID, relativePath string) (string, bool) {
	query := `
		SELECT f.drive_folder_id 
		FROM upload_folders f 
		JOIN upload_sessions s ON f.session_id = s.id 
		WHERE s.dest_folder_id = ? 
		  AND f.relative_path = ? 
		  AND f.drive_folder_id != ''
		ORDER BY s.created_at DESC
		LIMIT 1`
	var driveFolderID string
	err := DB.QueryRow(query, destFolderID, relativePath).Scan(&driveFolderID)
	if err != nil {
		return "", false
	}
	return driveFolderID, true
}

// GetIncompleteSessions retrieves all incomplete upload sessions
func GetIncompleteSessions() ([]UploadSession, error) {
	query := `SELECT id, name, local_path, dest_folder_id, status, created_at FROM upload_sessions WHERE status = 'incomplete' ORDER BY created_at DESC`
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []UploadSession
	for rows.Next() {
		var s UploadSession
		var createdAtStr string
		if err := rows.Scan(&s.ID, &s.Name, &s.LocalPath, &s.DestFolderID, &s.Status, &createdAtStr); err != nil {
			return nil, err
		}
		// SQLite returns string for datetime if using standard text format
		s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		if s.CreatedAt.IsZero() {
			// fallback parse for timezone-included formats if sqlite configured differently
			s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// GetSessionFiles retrieves all files for a given session
func GetSessionFiles(sessionID int64) ([]UploadFile, error) {
	query := `SELECT id, session_id, local_path, relative_path, dest_folder_relative_path, size, status, drive_file_id, error_msg, updated_at FROM upload_files WHERE session_id = ?`
	rows, err := DB.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []UploadFile
	for rows.Next() {
		var f UploadFile
		var updatedAtStr string
		if err := rows.Scan(&f.ID, &f.SessionID, &f.LocalPath, &f.RelativePath, &f.DestFolderRelativePath, &f.Size, &f.Status, &f.DriveFileID, &f.ErrorMsg, &updatedAtStr); err != nil {
			return nil, err
		}
		f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAtStr)
		if f.UpdatedAt.IsZero() {
			f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAtStr)
		}
		files = append(files, f)
	}
	return files, nil
}

// GetSessionFolders retrieves all folders for a given session
func GetSessionFolders(sessionID int64) ([]UploadFolder, error) {
	query := `SELECT id, session_id, relative_path, drive_folder_id FROM upload_folders WHERE session_id = ?`
	rows, err := DB.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []UploadFolder
	for rows.Next() {
		var f UploadFolder
		if err := rows.Scan(&f.ID, &f.SessionID, &f.RelativePath, &f.DriveFolderID); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, nil
}

// UpdateFolderDriveID updates the drive ID of a folder inside a session
func UpdateFolderDriveID(sessionID int64, relativePath, driveFolderID string) error {
	query := `UPDATE upload_folders SET drive_folder_id = ? WHERE session_id = ? AND relative_path = ?`
	_, err := DB.Exec(query, driveFolderID, sessionID, relativePath)
	return err
}

// UpdateFileStatus updates status and optional drive file ID or error message
func UpdateFileStatus(fileID int64, status, driveFileID, errMsg string) error {
	query := `UPDATE upload_files SET status = ?, drive_file_id = ?, error_msg = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := DB.Exec(query, status, driveFileID, errMsg, fileID)
	return err
}

// UpdateSessionStatus updates status of a session
func UpdateSessionStatus(sessionID int64, status string) error {
	query := `UPDATE upload_sessions SET status = ? WHERE id = ?`
	_, err := DB.Exec(query, status, sessionID)
	return err
}
