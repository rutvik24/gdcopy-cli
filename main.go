package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gdcopy/internal/drive"
	"gdcopy/internal/ui"
)

// Version holds the current version of the tool.
// This can be set at compile time using:
// go build -ldflags "-X main.Version=v1.1.0"
var Version = "v1.1.0"

func main() {
	// Let user select or create a database path
	dbPath, err := selectDatabasePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[Database Setup Error] %v\n", err)
		os.Exit(1)
	}

	// Initialize SQLite DB for upload tracking
	if err := drive.InitDB(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "\n[Database Error] %v\n", err)
		os.Exit(1)
	}
	defer drive.CloseDB()

	// Set up cancellable context for graceful termination
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up channel to capture termination signals (like Ctrl+C / SIGINT or SIGTERM)
	cleanupChan := make(chan os.Signal, 1)
	signal.Notify(cleanupChan, os.Interrupt, syscall.SIGTERM)

	// Capture interrupt signal in a goroutine to clean up and exit
	go func() {
		<-cleanupChan
		fmt.Println("\nProcess termination signal received. Cleaning up active uploads...")
		cancel()

		// A second interrupt signal will force immediate exit
		<-cleanupChan
		fmt.Println("\nForced exit.")
		os.Exit(1)
	}()

	// Parse CLI flags
	versionFlag := flag.Bool("version", false, "Print version and exit")
	vFlag := flag.Bool("v", false, "Print version and exit (shorthand)")
	credFlag := flag.String("credentials", "", "Path to Google OAuth credentials.json file")
	cFlag := flag.String("c", "", "Path to Google OAuth credentials.json file (shorthand)")

	flag.Parse()

	// Handle version flags
	if *versionFlag || *vFlag {
		fmt.Printf("gdcopy %s\n", Version)
		os.Exit(0)
	}

	// Determine credentials file path
	credentialsPath := "credentials.json"
	if *credFlag != "" {
		credentialsPath = *credFlag
	} else if *cFlag != "" {
		credentialsPath = *cFlag
	}

	fmt.Println("=========================================================")
	fmt.Println("                         GDCOPY                          ")
	fmt.Printf("                  Version: %s\n", Version)
	fmt.Println("=========================================================")

	// 1. Initialize Google Drive Service (triggering OAuth if necessary)
	srv, err := drive.GetDriveService(credentialsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[Initialization Error] %v\n", err)
		os.Exit(1)
	}

	// 2. Start the interactive terminal UI
	uiState := ui.NewUIState(ctx, srv)
	uiState.Run()
}

// selectDatabasePath prompts the user to select an existing .db file or input a new one starting from the user's home directory.
func selectDatabasePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	paths, err := ui.SelectLocalPath(homeDir, true)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no database file selected")
	}
	return paths[0], nil
}
