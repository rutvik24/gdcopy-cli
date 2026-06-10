package drive

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/manifoldco/promptui"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// tokenCacheFile returns the path where the token should be saved.
func tokenCacheFile() string {
	return "token.json"
}

func getEncryptionKey() ([]byte, error) {
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()
	salt := "gdcopy-salt-983120"
	data := hostname + homeDir + salt
	hash := sha256.Sum256([]byte(data))
	return hash[:], nil
}

func encrypt(plaintext []byte) ([]byte, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

func decrypt(ciphertext []byte) ([]byte, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

// tokenFromFile retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	cipherText, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	plainText, err := decrypt(cipherText)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt token file (maybe key changed or file corrupted): %v", err)
	}
	tok := &oauth2.Token{}
	err = json.Unmarshal(plainText, tok)
	return tok, err
}

// saveToken saves a token to a file path.
func saveToken(path string, token *oauth2.Token) error {
	fmt.Printf("Saving credential file to: %s\n", path)
	plainText, err := json.Marshal(token)
	if err != nil {
		return err
	}
	cipherText, err := encrypt(plainText)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %v", err)
	}
	defer f.Close()
	_, err = f.Write(cipherText)
	return err
}

// CleanupToken deletes the cached token file if it exists.
func CleanupToken() {
	tokFile := tokenCacheFile()
	if _, err := os.Stat(tokFile); err == nil {
		_ = os.Remove(tokFile)
	}
}

// openBrowser opens the specified URL in the default browser of the user.
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		fmt.Printf("Could not open browser automatically. Please open this link manually:\n%s\n", url)
	}
}

type savingTokenSource struct {
	source  oauth2.TokenSource
	tokFile string
	lastTok *oauth2.Token
	mu      sync.Mutex
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tok, err := s.source.Token()
	if err != nil {
		return nil, err
	}

	// If the token is different (e.g. new AccessToken), save it to file
	if s.lastTok == nil || tok.AccessToken != s.lastTok.AccessToken {
		err := saveToken(s.tokFile, tok)
		if err != nil {
			fmt.Printf("Warning: failed to save refreshed token to %s: %v\n", s.tokFile, err)
		}
		s.lastTok = tok
	}

	return tok, nil
}

// getClient retrieves a token, saves it, and returns the generated client.
func getClient(config *oauth2.Config) (*http.Client, error) {
	tokFile := tokenCacheFile()
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		// Token not found or invalid, obtain new token via browser login
		tok, err = getTokenFromWeb(config)
		if err != nil {
			return nil, err
		}

		err = saveToken(tokFile, tok)
		if err != nil {
			return nil, err
		}
	}

	// Create a token source that auto-refreshes using config
	baseSource := config.TokenSource(context.Background(), tok)

	// Wrap it in our savingTokenSource to auto-save to file when refreshed
	savingSource := &savingTokenSource{
		source:  baseSource,
		tokFile: tokFile,
		lastTok: tok,
	}

	// Return a client using the saving token source
	return oauth2.NewClient(context.Background(), savingSource), nil
}

// getTokenFromWeb starts a local webserver and redirects the user to Google's OAuth consent screen.
// Once authorized, it retrieves the auth code from the callback and exchanges it for a token.
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	// Listen on a random available port on localhost
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start local listener: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	
	// Update redirect URL to use the dynamic local port
	config.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// Channel to receive the authorization code
	codeChan := make(chan string)
	errChan := make(chan error)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body><h3>Error: Missing 'code' parameter.</h3></body></html>")
			errChan <- fmt.Errorf("missing authorization code in callback")
			return
		}

		// Nice success page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `
			<html>
			<head>
				<title>Authentication Successful</title>
				<style>
					body {
						font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
						text-align: center;
						padding: 50px;
						background-color: #f8f9fa;
						color: #343a40;
					}
					.container {
						max-width: 500px;
						margin: 0 auto;
						background: white;
						padding: 40px;
						border-radius: 8px;
						box-shadow: 0 4px 12px rgba(0,0,0,0.1);
					}
					h1 { color: #28a745; margin-bottom: 20px; }
					p { font-size: 16px; line-height: 1.5; }
				</style>
			</head>
			<body>
				<div class="container">
					<h1>Authentication Successful!</h1>
					<p>You have successfully logged in with Google. You can now close this tab and return to the terminal.</p>
				</div>
			</body>
			</html>
		`)

		codeChan <- code
	})

	srv := &http.Server{
		Handler: mux,
	}

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Generate authorization URL
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("\n--- Google Authentication ---\n")
	fmt.Printf("Opening your browser to complete Google Sign-In...\n")
	fmt.Printf("If it doesn't open, copy and paste this URL into your browser:\n%s\n\n", authURL)

	openBrowser(authURL)

	// Wait for authorization code or error with a timeout of 3 minutes
	select {
	case code := <-codeChan:
		// Shut down the server
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)

		// Exchange authorization code for token
		tok, err := config.Exchange(context.Background(), code)
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve token from web: %v", err)
		}
		return tok, nil

	case err := <-errChan:
		return nil, fmt.Errorf("local server error: %v", err)

	case <-time.After(3 * time.Minute):
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return nil, fmt.Errorf("authentication timed out after 3 minutes")
	}
}

// GetDriveService initializes the Google Drive API service.
func GetDriveService(credPath string) (*drive.Service, error) {
	var credBytes []byte
	var err error

	for {
		credBytes, err = os.ReadFile(credPath)
		if err == nil {
			break
		}

		fmt.Printf("\n[Error] Unable to read credentials file at: %s\n", credPath)

		prompt := promptui.Select{
			Label: "Credentials file missing or unreadable. Choose an action",
			Items: []string{
				"Provide a custom path to credentials.json",
				"Show step-by-step setup and permission instructions",
				"Exit",
			},
		}

		idx, _, err := prompt.Run()
		if err != nil {
			return nil, fmt.Errorf("credentials setup aborted: %v", err)
		}

		switch idx {
		case 0:
			result, err := selectLocalFile("Select the credentials.json file")
			if err != nil {
				return nil, fmt.Errorf("path selection aborted: %v", err)
			}
			credPath = result
		case 1:
			printCredentialsSetupInstructions(filepath.Dir(tokenCacheFile()))
		case 2:
			return nil, fmt.Errorf("credentials.json is required to run the Google Drive API service")
		}
	}

	// Request full drive access to allow file copying, folder creation, and renaming.
	config, err := google.ConfigFromJSON(credBytes, drive.DriveScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	client, err := getClient(config)
	if err != nil {
		return nil, err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve Drive client: %v", err)
	}

	return srv, nil
}

// printCredentialsSetupInstructions outputs details on obtaining client secret.
func printCredentialsSetupInstructions(tokenDir string) {
	fmt.Println("\n=======================================================================")
	fmt.Println("             GOOGLE DRIVE API CREDENTIALS SETUP INSTRUCTIONS           ")
	fmt.Println("=======================================================================")
	fmt.Println("To run this application, you must provide a 'credentials.json' file.")
	fmt.Println("Please follow these steps to get one and configure permissions:")
	fmt.Println()
	fmt.Println("1. Go to the Google Cloud Console: https://console.cloud.google.com/")
	fmt.Println("2. Create a new project (e.g., 'gdcopy').")
	fmt.Println("3. Enable the 'Google Drive API' in the API Library.")
	fmt.Println("4. Set up the OAuth Consent Screen:")
	fmt.Println("   a. Choose User Type: 'External'.")
	fmt.Println("   b. Fill in required App Information.")
	fmt.Println("   c. Add the scope: 'https://www.googleapis.com/auth/drive'.")
	fmt.Println("   d. Under 'Test users', add your Google email address.")
	fmt.Println("5. Create OAuth Credentials:")
	fmt.Println("   a. Go to Credentials -> Create Credentials -> OAuth client ID.")
	fmt.Println("   b. Select Application Type: 'Desktop App'.")
	fmt.Println("   c. Give it a name and click 'Create'.")
	fmt.Println("6. Download the Client configuration JSON file, rename it to 'credentials.json',")
	fmt.Println("   and place it in this directory or enter its path when prompted.")
	fmt.Println("=======================================================================")
	fmt.Println("\nPress Enter to continue back to the options menu...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

// selectLocalFile allows the user to browse directories and select a file interactively.
func selectLocalFile(label string) (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		dir, err = os.Getwd()
		if err != nil {
			dir = "."
		}
	}

	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Printf("Error reading directory: %v\n", err)
			dir = filepath.Dir(dir)
			continue
		}

		type FileItem struct {
			Name  string
			Path  string
			IsDir bool
		}

		var items []FileItem

		// 1. Add "Go Up" option if we are not at root
		parent := filepath.Dir(dir)
		if parent != dir {
			items = append(items, FileItem{
				Name:  "↩ .. (Go Up)",
				Path:  parent,
				IsDir: true,
			})
		}

		// 2. Add manual input option
		items = append(items, FileItem{
			Name:  "⌨ [Type path manually]",
			Path:  "",
			IsDir: false,
		})

		// 3. Add quick jump shortcuts
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			if dir != home {
				items = append(items, FileItem{
					Name:  "🏠 [Go to Home]",
					Path:  home,
					IsDir: true,
				})
			}
			downloadsPath := filepath.Join(home, "Downloads")
			if dir != downloadsPath {
				if _, err := os.Stat(downloadsPath); err == nil {
					items = append(items, FileItem{
						Name:  "📥 [Go to Downloads]",
						Path:  downloadsPath,
						IsDir: true,
					})
				}
			}
			documentsPath := filepath.Join(home, "Documents")
			if dir != documentsPath {
				if _, err := os.Stat(documentsPath); err == nil {
					items = append(items, FileItem{
						Name:  "📁 [Go to Documents]",
						Path:  documentsPath,
						IsDir: true,
					})
				}
			}
		}

		cwd, cwdErr := os.Getwd()
		if cwdErr == nil && dir != cwd {
			items = append(items, FileItem{
				Name:  "💻 [Go to Project Root]",
				Path:  cwd,
				IsDir: true,
			})
		}

		// 4. Separate folders and files
		var folders []FileItem
		var files []FileItem

		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}

			fullPath := filepath.Join(dir, name)
			if entry.IsDir() {
				folders = append(folders, FileItem{
					Name:  "📂 " + name + "/",
					Path:  fullPath,
					IsDir: true,
				})
			} else {
				icon := "📄 "
				if strings.HasSuffix(strings.ToLower(name), ".json") {
					icon = "🔑 "
				}
				files = append(files, FileItem{
					Name:  icon + name,
					Path:  fullPath,
					IsDir: false,
				})
			}
		}

		items = append(items, folders...)
		items = append(items, files...)

		var labels []string
		for _, item := range items {
			labels = append(labels, item.Name)
		}

		templates := &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "👉 {{ . | cyan }}",
			Inactive: "   {{ . }}",
			Selected: "👉 {{ . | green | bold }}",
		}

		prompt := promptui.Select{
			Label:     fmt.Sprintf("%s (Current Dir: %s)", label, dir),
			Items:     labels,
			Templates: templates,
			Size:      15,
		}

		idx, _, err := prompt.Run()
		if err != nil {
			return "", err
		}

		selected := items[idx]
		if selected.Name == "⌨ [Type path manually]" {
			pathPrompt := promptui.Prompt{
				Label: "Enter file path to credentials.json (leave empty/Ctrl+C to go back)",
			}
			res, err := pathPrompt.Run()
			if err != nil {
				// Ctrl+C or interrupt goes back to directory browsing
				continue
			}
			resTrimmed := strings.TrimSpace(res)
			if resTrimmed == "" || strings.ToLower(resTrimmed) == "back" {
				continue
			}
			return resTrimmed, nil
		}

		if selected.IsDir {
			dir = selected.Path
		} else {
			return selected.Path, nil
		}
	}
}
