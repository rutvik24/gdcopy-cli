package main

import (
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
// go build -ldflags "-X main.Version=v1.0.0"
var Version = "v1.0.0"

func main() {
	// Set up channel to capture termination signals (like Ctrl+C / SIGINT or SIGTERM)
	cleanupChan := make(chan os.Signal, 1)
	signal.Notify(cleanupChan, os.Interrupt, syscall.SIGTERM)

	// Make sure we cleanup the token file when the program exits normally
	defer drive.CleanupToken()

	// Capture interrupt signal in a goroutine to clean up and exit
	go func() {
		<-cleanupChan
		drive.CleanupToken()
		fmt.Println("\nProcess terminated. Secure token cleaned up.")
		os.Exit(0)
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
	uiState := ui.NewUIState(srv)
	uiState.Run()
}
