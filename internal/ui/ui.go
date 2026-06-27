package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
	gdrive "google.golang.org/api/drive/v3"
	"gdcopy/internal/drive"
)

// Location represents a step in the navigation path
type Location struct {
	ID        string // Google Drive Folder ID
	Name      string // Folder Name
	IsVirtual bool   // True if it's a virtual menu like "Shared with me" root
}

// UIState maintains the state of the CLI application
type UIState struct {
	ctx          context.Context
	srv          *gdrive.Service
	pathStack    []Location
	sourceFolder *drive.Folder
	sourceFiles  []*gdrive.File
	destFolder   *drive.Folder
	shouldExit   bool
}

// NewUIState initializes a new UIState
func NewUIState(ctx context.Context, srv *gdrive.Service) *UIState {
	return &UIState{
		ctx: ctx,
		srv: srv,
		pathStack: []Location{
			{ID: "main_root", Name: "Home", IsVirtual: true},
		},
	}
}

// CurrentLocation returns the active location
func (ui *UIState) CurrentLocation() Location {
	if len(ui.pathStack) == 0 {
		return Location{ID: "main_root", Name: "Home", IsVirtual: true}
	}
	return ui.pathStack[len(ui.pathStack)-1]
}

// Run starts the interactive loop
func (ui *UIState) Run() {
	for {
		curr := ui.CurrentLocation()
		var menuItems []MenuItem
		var err error

		// Build menu items depending on current folder location
		if curr.ID == "main_root" {
			menuItems = ui.buildMainMenu()
		} else if curr.ID == "shared_with_me_root" {
			menuItems, err = ui.buildSharedWithMeMenu()
			if err != nil {
				fmt.Printf("Error fetching shared folders: %v\n", err)
				ui.pressEnterToContinue()
				ui.pathStack = ui.pathStack[:len(ui.pathStack)-1] // Go back
				continue
			}
		} else {
			menuItems, err = ui.buildFolderMenu(curr)
			if err != nil {
				fmt.Printf("Error fetching folder contents: %v\n", err)
				ui.pressEnterToContinue()
				ui.pathStack = ui.pathStack[:len(ui.pathStack)-1] // Go back
				continue
			}
		}

		// Prepare promptui select items
		type Option struct {
			Label string
			Index int
		}

		var options []Option
		for i, item := range menuItems {
			options = append(options, Option{
				Label: item.Label,
				Index: i,
			})
		}

		// Format header label
		header := ui.getStatusLabel()
		
		templates := &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "\U0001F449 {{ .Label | cyan }}",
			Inactive: "   {{ .Label }}",
			Selected: "\U0001F449 {{ .Label | green | bold }}",
		}

		searcher := func(input string, index int) bool {
			opt := options[index]
			return strings.Contains(strings.ToLower(opt.Label), strings.ToLower(input))
		}

		prompt := promptui.Select{
			Label:     header,
			Items:     options,
			Templates: templates,
			Size:      15,
			Searcher:  searcher,
		}

		idx, _, err := prompt.Run()
		if err != nil {
			if err == promptui.ErrInterrupt {
				fmt.Println("Exiting application...")
				return
			}
			fmt.Printf("Error: %v\n", err)
			continue
		}

		// Execute action
		menuItems[idx].Action()
		if ui.shouldExit {
			return
		}
	}
}

// MenuItem represents an entry in the prompt selection list
type MenuItem struct {
	Label  string
	Action func()
}

// buildMainMenu builds the top-level home menu
func (ui *UIState) buildMainMenu() []MenuItem {
	var items []MenuItem

	items = append(items, MenuItem{
		Label: "[My Drive]",
		Action: func() {
			ui.pathStack = append(ui.pathStack, Location{ID: "root", Name: "My Drive", IsVirtual: false})
		},
	})

	items = append(items, MenuItem{
		Label: "[Shared with me]",
		Action: func() {
			ui.pathStack = append(ui.pathStack, Location{ID: "shared_with_me_root", Name: "Shared with me", IsVirtual: true})
		},
	})

	if ui.sourceFolder != nil || len(ui.sourceFiles) > 0 || ui.destFolder != nil {
		items = append(items, MenuItem{
			Label: "Clear selections",
			Action: func() {
				ui.sourceFolder = nil
				ui.sourceFiles = nil
				ui.destFolder = nil
				fmt.Println("Selections cleared!")
			},
		})
	}

	// Check for interrupted uploads
	if incompleteSessions, err := drive.GetIncompleteSessions(); err == nil && len(incompleteSessions) > 0 {
		items = append(items, MenuItem{
			Label: fmt.Sprintf("[Resume interrupted upload (%d sessions)]", len(incompleteSessions)),
			Action: func() {
				ui.handleResumeUploadMenu(incompleteSessions)
			},
		})
	}

	items = append(items, MenuItem{
		Label: "Exit",
		Action: func() {
			fmt.Println("Goodbye!")
			ui.shouldExit = true
		},
	})

	return items
}

// buildSharedWithMeMenu lists all folders shared with the user
func (ui *UIState) buildSharedWithMeMenu() ([]MenuItem, error) {
	var items []MenuItem

	// Add Back option
	items = append(items, MenuItem{
		Label: ".. (Back to Home)",
		Action: func() {
			ui.pathStack = ui.pathStack[:len(ui.pathStack)-1]
		},
	})

	folders, err := drive.ListSharedWithMeFolders(ui.srv)
	if err != nil {
		return nil, err
	}

	for _, f := range folders {
		folderCopy := f // local copy for closure
		items = append(items, MenuItem{
			Label: fmt.Sprintf("\U0001F4C1 %s [Shared]", f.Name),
			Action: func() {
				ui.pathStack = append(ui.pathStack, Location{
					ID:        folderCopy.ID,
					Name:      folderCopy.Name,
					IsVirtual: false,
				})
			},
		})
	}

	return items, nil
}

// buildFolderMenu constructs the actions and subfolders menu for a standard Drive folder
func (ui *UIState) buildFolderMenu(curr Location) ([]MenuItem, error) {
	var items []MenuItem

	// 1. Selection & Utility options
	items = append(items, MenuItem{
		Label: fmt.Sprintf("-> SELECT folder '%s' as SOURCE for copy", curr.Name),
		Action: func() {
			ui.sourceFolder = &drive.Folder{
				ID:           curr.ID,
				Name:         curr.Name,
				SharedWithMe: false, // will copy regardless
			}
			ui.sourceFiles = nil
			fmt.Printf("Source folder selected: %s\n", curr.Name)
			ui.checkAndTriggerCopy()
		},
	})

	items = append(items, MenuItem{
		Label: "-> SELECT files in this folder as SOURCE for copy",
		Action: func() {
			ui.handleSelectFilesAsSource(curr.ID)
		},
	})

	items = append(items, MenuItem{
		Label: fmt.Sprintf("-> SELECT folder '%s' as DESTINATION for copy", curr.Name),
		Action: func() {
			ui.destFolder = &drive.Folder{
				ID:           curr.ID,
				Name:         curr.Name,
				SharedWithMe: false,
			}
			fmt.Printf("Destination folder selected: %s\n", curr.Name)
			ui.checkAndTriggerCopy()
		},
	})

	items = append(items, MenuItem{
		Label: "-> BULK RENAME files in this folder",
		Action: func() {
			ui.handleBulkRename(curr.ID)
		},
	})

	items = append(items, MenuItem{
		Label: "-> VIEW files in this folder",
		Action: func() {
			ui.handleViewFiles(curr.ID)
		},
	})

	items = append(items, MenuItem{
		Label: "-> CREATE a new subfolder in this folder",
		Action: func() {
			ui.handleCreateFolder(curr.ID)
		},
	})

	items = append(items, MenuItem{
		Label: "-> UPLOAD a local file or folder to this folder",
		Action: func() {
			ui.handleUploadLocalPath(curr.ID)
		},
	})

	// Check for interrupted uploads
	if incompleteSessions, err := drive.GetIncompleteSessions(); err == nil && len(incompleteSessions) > 0 {
		items = append(items, MenuItem{
			Label: fmt.Sprintf("-> RESUME interrupted upload (%d sessions pending)", len(incompleteSessions)),
			Action: func() {
				ui.handleResumeUploadMenu(incompleteSessions)
			},
		})
	}

	// Add Back option
	items = append(items, MenuItem{
		Label: ".. (Go Up)",
		Action: func() {
			ui.pathStack = ui.pathStack[:len(ui.pathStack)-1]
		},
	})

	// Fetch subfolders of this directory
	subfolders, err := drive.ListFoldersInParent(ui.srv, curr.ID)
	if err != nil {
		return nil, err
	}

	for _, sf := range subfolders {
		folderCopy := sf // local copy for closure
		items = append(items, MenuItem{
			Label: fmt.Sprintf("\U0001F4C1 %s", sf.Name),
			Action: func() {
				ui.pathStack = append(ui.pathStack, Location{
					ID:        folderCopy.ID,
					Name:      folderCopy.Name,
					IsVirtual: false,
				})
			},
		})
	}

	return items, nil
}

// getStatusLabel generates a status breadcrumb layout
func (ui *UIState) getStatusLabel() string {
	var pathNames []string
	for _, p := range ui.pathStack {
		pathNames = append(pathNames, p.Name)
	}
	pathStr := strings.Join(pathNames, " > ")

	srcStr := "<none>"
	if ui.sourceFolder != nil {
		srcStr = fmt.Sprintf("Folder '%s'", ui.sourceFolder.Name)
	} else if len(ui.sourceFiles) > 0 {
		srcStr = fmt.Sprintf("%d file(s) selected", len(ui.sourceFiles))
	}

	destStr := "<none>"
	if ui.destFolder != nil {
		destStr = ui.destFolder.Name
	}

	var statusLines []string
	statusLines = append(statusLines, "")
	statusLines = append(statusLines, "=================================================================")
	statusLines = append(statusLines, fmt.Sprintf(" PATH:       %s", pathStr))
	statusLines = append(statusLines, fmt.Sprintf(" SOURCE:     %s", srcStr))
	statusLines = append(statusLines, fmt.Sprintf(" DEST:       %s", destStr))
	statusLines = append(statusLines, "=================================================================")
	statusLines = append(statusLines, "Select an action or subfolder (type to search/filter):")

	return strings.Join(statusLines, "\n")
}

// checkAndTriggerCopy evaluates if both source and destination are set and triggers the copy process
func (ui *UIState) checkAndTriggerCopy() {
	if (ui.sourceFolder == nil && len(ui.sourceFiles) == 0) || ui.destFolder == nil {
		return
	}

	var strategy string

	if ui.sourceFolder != nil {
		fmt.Printf("\n--- Folder Copy Setup ---\n")
		fmt.Printf("Source Folder:      %s (ID: %s)\n", ui.sourceFolder.Name, ui.sourceFolder.ID)
		fmt.Printf("Destination Folder: %s (ID: %s)\n", ui.destFolder.Name, ui.destFolder.ID)

		prompt := promptui.Prompt{
			Label:     fmt.Sprintf("Enter a custom name for the new folder copy (press Enter to keep '%s')", ui.sourceFolder.Name),
			Default:   ui.sourceFolder.Name,
			AllowEdit: true,
		}

		folderName, err := prompt.Run()
		if err != nil {
			fmt.Println("Copy operation cancelled.")
			ui.sourceFolder = nil
			ui.sourceFiles = nil
			ui.destFolder = nil
			return
		}

		confirmPrompt := promptui.Prompt{
			Label:     fmt.Sprintf("Confirm copying '%s' to '%s'? (y/n)", ui.sourceFolder.Name, ui.destFolder.Name),
			Default:   "n",
			AllowEdit: false,
		}

		confirm, err := confirmPrompt.Run()
		if err != nil || strings.ToLower(confirm) != "y" {
			fmt.Println("Copy operation cancelled.")
			ui.sourceFolder = nil
			ui.sourceFiles = nil
			ui.destFolder = nil
			return
		}

		collisionPrompt := promptui.Select{
			Label: "Select name collision strategy for duplicate files",
			Items: []string{
				"Skip (Keep existing file, do not copy)",
				"Overwrite (Replace existing file in destination)",
				"Add Suffix (Keep both, rename new copy as 'filename (1).ext')",
			},
		}

		idx, _, err := collisionPrompt.Run()
		if err != nil {
			fmt.Println("Copy operation cancelled.")
			ui.sourceFolder = nil
			ui.sourceFiles = nil
			ui.destFolder = nil
			return
		}

		switch idx {
		case 0:
			strategy = "skip"
		case 1:
			strategy = "overwrite"
		case 2:
			strategy = "suffix"
		}

		fmt.Println("\nStarting recursive copy...")
		newFolderID, err := drive.CopyFolderRecursive(ui.srv, ui.sourceFolder.ID, ui.destFolder.ID, folderName, strategy, func(logMsg string) {
			fmt.Println(" >", logMsg)
		})

		if err != nil {
			fmt.Printf("\nError during copy: %v\n", err)
		} else {
			fmt.Printf("\nSuccess! Folder copied. New Folder ID: %s\n", newFolderID)
		}

	} else {
		fmt.Printf("\n--- File Copy Setup ---\n")
		fmt.Printf("Source Files (%d selected):\n", len(ui.sourceFiles))
		for i, f := range ui.sourceFiles {
			if i < 5 {
				fmt.Printf("  - %s (ID: %s)\n", f.Name, f.Id)
			}
		}
		if len(ui.sourceFiles) > 5 {
			fmt.Printf("  ... and %d more\n", len(ui.sourceFiles)-5)
		}
		fmt.Printf("Destination Folder: %s (ID: %s)\n", ui.destFolder.Name, ui.destFolder.ID)

		confirmPrompt := promptui.Prompt{
			Label:     fmt.Sprintf("Confirm copying %d file(s) directly into '%s'? (y/n)", len(ui.sourceFiles), ui.destFolder.Name),
			Default:   "n",
			AllowEdit: false,
		}

		confirm, err := confirmPrompt.Run()
		if err != nil || strings.ToLower(confirm) != "y" {
			fmt.Println("Copy operation cancelled.")
			ui.sourceFolder = nil
			ui.sourceFiles = nil
			ui.destFolder = nil
			return
		}

		collisionPrompt := promptui.Select{
			Label: "Select name collision strategy for duplicate files",
			Items: []string{
				"Skip (Keep existing file, do not copy)",
				"Overwrite (Replace existing file in destination)",
				"Add Suffix (Keep both, rename new copy as 'filename (1).ext')",
			},
		}

		idx, _, err := collisionPrompt.Run()
		if err != nil {
			fmt.Println("Copy operation cancelled.")
			ui.sourceFolder = nil
			ui.sourceFiles = nil
			ui.destFolder = nil
			return
		}

		switch idx {
		case 0:
			strategy = "skip"
		case 1:
			strategy = "overwrite"
		case 2:
			strategy = "suffix"
		}

		fmt.Println("\nStarting copy of files...")
		err = drive.CopyFiles(ui.srv, ui.sourceFiles, ui.destFolder.ID, strategy, func(logMsg string) {
			fmt.Println(" >", logMsg)
		})

		if err != nil {
			fmt.Printf("\nError during copy: %v\n", err)
		} else {
			fmt.Println("\nSuccess! Files copied.")
		}
	}

	// Reset selections after copying
	ui.sourceFolder = nil
	ui.sourceFiles = nil
	ui.destFolder = nil
	ui.pressEnterToContinue()
}

// handleViewFiles displays the list of files in the current folder
func (ui *UIState) handleViewFiles(folderID string) {
	fmt.Println("\nLoading files...")
	items, err := drive.ListFilesAndFolders(ui.srv, folderID)
	if err != nil {
		fmt.Printf("Error fetching files: %v\n", err)
		ui.pressEnterToContinue()
		return
	}

	fmt.Printf("\n--- Files in Folder ---\n")
	hasFiles := false
	for _, item := range items {
		if item.MimeType != "application/vnd.google-apps.folder" {
			hasFiles = true
			sizeStr := "Unknown Size"
			if item.Size > 0 {
				sizeStr = formatBytes(item.Size)
			}
			fmt.Printf(" - %s (%s)\n", item.Name, sizeStr)
		}
	}

	if !hasFiles {
		fmt.Println(" (No files found in this folder, only subfolders or empty)")
	}
	fmt.Println("-----------------------")
	ui.pressEnterToContinue()
}

// handleBulkRename walks the user through renaming files in the folder
func (ui *UIState) handleBulkRename(folderID string) {
	fmt.Println("\nLoading folder items...")
	items, err := drive.ListFilesAndFolders(ui.srv, folderID)
	if err != nil {
		fmt.Printf("Error fetching files: %v\n", err)
		ui.pressEnterToContinue()
		return
	}

	// Filter for files only
	var files []*gdrive.File
	for _, f := range items {
		if f.MimeType != "application/vnd.google-apps.folder" {
			files = append(files, f)
		}
	}

	if len(files) == 0 {
		fmt.Println("\nNo files (excluding folders) in this folder to rename.")
		ui.pressEnterToContinue()
		return
	}

	fmt.Printf("\nFound %d files to rename.\n", len(files))

	// Choose Renaming Strategy
	strategyPrompt := promptui.Select{
		Label: "Select renaming strategy",
		Items: []string{
			"1. Find and Replace",
			"2. Add Prefix",
			"3. Add Suffix",
			"4. Sequential Numbering (e.g. photo_01.jpg)",
			"5. Cancel",
		},
	}

	idx, _, err := strategyPrompt.Run()
	if err != nil || idx == 4 {
		fmt.Println("Renaming cancelled.")
		return
	}

	var strategy, param1, param2 string

	switch idx {
	case 0:
		strategy = "replace"
		findPrompt := promptui.Prompt{Label: "Enter string to FIND"}
		param1, err = findPrompt.Run()
		if err != nil || param1 == "" {
			fmt.Println("Invalid parameter. Renaming cancelled.")
			return
		}
		replacePrompt := promptui.Prompt{Label: "Enter replacement string (can be empty)"}
		param2, err = replacePrompt.Run()
		if err != nil {
			fmt.Println("Renaming cancelled.")
			return
		}
	case 1:
		strategy = "prefix"
		prefixPrompt := promptui.Prompt{Label: "Enter prefix to ADD"}
		param1, err = prefixPrompt.Run()
		if err != nil || param1 == "" {
			fmt.Println("Invalid prefix. Renaming cancelled.")
			return
		}
	case 2:
		strategy = "suffix"
		suffixPrompt := promptui.Prompt{Label: "Enter suffix to ADD (inserted before file extension)"}
		param1, err = suffixPrompt.Run()
		if err != nil || param1 == "" {
			fmt.Println("Invalid suffix. Renaming cancelled.")
			return
		}
	case 3:
		strategy = "number"
		basePrompt := promptui.Prompt{Label: "Enter base filename (e.g. 'holiday_photo')"}
		param1, err = basePrompt.Run()
		if err != nil || param1 == "" {
			fmt.Println("Invalid base filename. Renaming cancelled.")
			return
		}
		startPrompt := promptui.Prompt{
			Label:    "Enter starting index number",
			Default:  "1",
			Validate: func(input string) error {
				_, err := strconv.Atoi(input)
				if err != nil {
					return fmt.Errorf("must be a valid integer")
				}
				return nil
			},
		}
		param2, err = startPrompt.Run()
		if err != nil {
			fmt.Println("Renaming cancelled.")
			return
		}
	}

	// 1. Dry Run / Preview
	previewResults := drive.RenameFiles(ui.srv, files, strategy, param1, param2, true)
	if len(previewResults) == 0 {
		fmt.Println("\nNo file names would change based on your criteria.")
		ui.pressEnterToContinue()
		return
	}

	fmt.Printf("\n--- Rename Preview (%d files changing) ---\n", len(previewResults))
	for _, res := range previewResults {
		fmt.Printf(" %s\n   -> %s\n", res.OldName, res.NewName)
	}
	fmt.Println("--------------------------------------------")

	confirmPrompt := promptui.Prompt{
		Label:     "Do you want to apply these changes? (y/n)",
		Default:   "n",
		AllowEdit: false,
	}

	confirm, err := confirmPrompt.Run()
	if err != nil || strings.ToLower(confirm) != "y" {
		fmt.Println("Renaming cancelled.")
		return
	}

	// 2. Perform actual renaming
	fmt.Println("\nRenaming files...")
	actualResults := drive.RenameFiles(ui.srv, files, strategy, param1, param2, false)

	successCount := 0
	failCount := 0
	for _, res := range actualResults {
		if res.Success {
			successCount++
		} else {
			failCount++
			fmt.Printf("Failed to rename '%s': %v\n", res.OldName, res.Error)
		}
	}

	fmt.Printf("\nRenaming completed! Success: %d, Failed: %d\n", successCount, failCount)
	ui.pressEnterToContinue()
}

// pressEnterToContinue pauses output until the user presses Enter
func (ui *UIState) pressEnterToContinue() {
	fmt.Print("\nPress Enter to continue...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

// formatBytes prints human-readable file sizes
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// handleCreateFolder prompts the user for a folder name and creates it under the parent folder
func (ui *UIState) handleCreateFolder(parentID string) {
	prompt := promptui.Prompt{
		Label: "Enter the name of the new folder",
		Validate: func(input string) error {
			if strings.TrimSpace(input) == "" {
				return fmt.Errorf("folder name cannot be empty")
			}
			return nil
		},
	}

	folderName, err := prompt.Run()
	if err != nil || folderName == "" {
		fmt.Println("Folder creation cancelled.")
		return
	}

	fmt.Printf("Creating folder '%s'...\n", folderName)
	newFolder, err := drive.CreateFolder(ui.srv, folderName, parentID)
	if err != nil {
		fmt.Printf("Error creating folder: %v\n", err)
	} else {
		fmt.Printf("Successfully created folder '%s' (ID: %s)!\n", folderName, newFolder.Id)
	}
	ui.pressEnterToContinue()
}

// handleSelectFilesAsSource presents a toggle-based file multi-selection menu
func (ui *UIState) handleSelectFilesAsSource(folderID string) {
	fmt.Println("\nLoading files...")
	items, err := drive.ListFilesAndFolders(ui.srv, folderID)
	if err != nil {
		fmt.Printf("Error fetching files: %v\n", err)
		ui.pressEnterToContinue()
		return
	}

	// Filter for files only
	var files []*gdrive.File
	for _, f := range items {
		if f.MimeType != "application/vnd.google-apps.folder" {
			files = append(files, f)
		}
	}

	if len(files) == 0 {
		fmt.Println("\nNo files (excluding folders) in this folder to copy.")
		ui.pressEnterToContinue()
		return
	}

	selected := make(map[string]bool)
	cursorPos := 0

	for {
		type FileOption struct {
			Label string
			ID    string
			Index int
		}

		var options []FileOption
		options = append(options, FileOption{Label: "[Confirm Selection]", ID: "confirm", Index: 0})
		options = append(options, FileOption{Label: "[Cancel]", ID: "cancel", Index: 1})

		for i, f := range files {
			status := "[ ]"
			if selected[f.Id] {
				status = "[\u2713]" // Checkmark symbol: ✓
			}
			sizeStr := "Unknown Size"
			if f.Size > 0 {
				sizeStr = formatBytes(f.Size)
			}
			options = append(options, FileOption{
				Label: fmt.Sprintf("%s %s (%s)", status, f.Name, sizeStr),
				ID:    f.Id,
				Index: i + 2,
			})
		}

		templates := &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "\U0001F449 {{ .Label | cyan }}",
			Inactive: "   {{ .Label }}",
			Selected: "\U0001F449 {{ .Label | green | bold }}",
		}

		searcher := func(input string, index int) bool {
			opt := options[index]
			return strings.Contains(strings.ToLower(opt.Label), strings.ToLower(input))
		}

		selCount := 0
		for _, sel := range selected {
			if sel {
				selCount++
			}
		}

		prompt := promptui.Select{
			Label:     fmt.Sprintf("Select files to copy (currently selected: %d file(s)). Select [Confirm Selection] when done.", selCount),
			Items:     options,
			Templates: templates,
			Size:      15,
			Searcher:  searcher,
			CursorPos: cursorPos,
		}

		idx, _, err := prompt.Run()
		if err != nil {
			if err == promptui.ErrInterrupt {
				fmt.Println("Selection cancelled.")
				return
			}
			fmt.Printf("Error: %v\n", err)
			continue
		}

		choice := options[idx]
		if choice.ID == "confirm" {
			if selCount == 0 {
				fmt.Println("No files selected. Please select at least one file or Cancel.")
				ui.pressEnterToContinue()
				continue
			}
			var selectedFiles []*gdrive.File
			for _, f := range files {
				if selected[f.Id] {
					selectedFiles = append(selectedFiles, f)
				}
			}
			ui.sourceFiles = selectedFiles
			ui.sourceFolder = nil
			fmt.Printf("\nSelected %d file(s) as source for copy.\n", len(selectedFiles))
			ui.checkAndTriggerCopy()
			return
		} else if choice.ID == "cancel" {
			fmt.Println("Selection cancelled.")
			return
		} else {
			// Toggle selection
			selected[choice.ID] = !selected[choice.ID]
			cursorPos = idx
		}
	}
}

// handleUploadLocalPath prompts the user for local paths and session name, and starts the upload session
func (ui *UIState) handleUploadLocalPath(parentFolderID string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	// 1. Select local paths interactively starting from current machine user's home dir
	localPaths, err := SelectLocalPath(homeDir, false)
	if err != nil {
		fmt.Printf("Upload cancelled: %v\n", err)
		return
	}

	// 2. Ask for a session name for identification
	namePrompt := promptui.Prompt{
		Label: "Enter a name for this upload session (for tracking/resuming)",
		Validate: func(input string) error {
			if strings.TrimSpace(input) == "" {
				return fmt.Errorf("session name cannot be empty")
			}
			return nil
		},
	}

	sessionName, err := namePrompt.Run()
	if err != nil {
		fmt.Println("Upload cancelled.")
		return
	}

	fmt.Printf("\nPreparing upload for %d item(s)...\n", len(localPaths))
	sessionID, err := drive.StartNewUploadSession(ui.srv, sessionName, localPaths, parentFolderID)
	if err != nil {
		fmt.Printf("Error preparing upload: %v\n", err)
		if ui.ctx.Err() != nil {
			ui.shouldExit = true
			return
		}
		ui.pressEnterToContinue()
		return
	}

	fmt.Printf("Starting upload session %d: '%s'...\n", sessionID, sessionName)
	err = drive.RunUploadSession(ui.ctx, ui.srv, sessionID, func(msg string) {
		fmt.Println(" >", msg)
	})
	if err != nil {
		fmt.Printf("\nUpload interrupted/failed: %v\n", err)
		if ui.ctx.Err() == nil {
			fmt.Println("You can resume this upload later from the main menu or this folder.")
		}
	} else {
		fmt.Println("\nUpload completed successfully!")
	}

	if ui.ctx.Err() != nil {
		ui.shouldExit = true
		return
	}
	ui.pressEnterToContinue()
}

// handleResumeUploadMenu displays a selection menu for incomplete sessions and resumes the chosen one
func (ui *UIState) handleResumeUploadMenu(sessions []drive.UploadSession) {
	type SessionOption struct {
		Label string
		ID    int64
	}

	var options []SessionOption
	for _, s := range sessions {
		files, err := drive.GetSessionFiles(s.ID)
		total := len(files)
		uploaded := 0
		if err == nil {
			for _, f := range files {
				if f.Status == "uploaded" {
					uploaded++
				}
			}
		}
		
		options = append(options, SessionOption{
			Label: fmt.Sprintf("%s (%d/%d uploaded, created: %s)", s.Name, uploaded, total, s.CreatedAt.Format("2006-01-02 15:04")),
			ID:    s.ID,
		})
	}

	options = append(options, SessionOption{Label: "[Back]", ID: -1})

	prompt := promptui.Select{
		Label: "Select an incomplete upload session to resume",
		Items: options,
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}",
			Active:   "\U0001F449 {{ .Label | cyan }}",
			Inactive: "   {{ .Label }}",
			Selected: "\U0001F449 {{ .Label | green | bold }}",
		},
	}

	idx, _, err := prompt.Run()
	if err != nil || options[idx].ID == -1 {
		return
	}

	sessionID := options[idx].ID
	
	fmt.Printf("\nResuming upload session %d...\n", sessionID)
	
	err = drive.RunUploadSession(ui.ctx, ui.srv, sessionID, func(msg string) {
		fmt.Println(" >", msg)
	})
	if err != nil {
		fmt.Printf("\nUpload interrupted/failed: %v\n", err)
	} else {
		fmt.Printf("\nUpload session %d completed successfully!\n", sessionID)
	}

	if ui.ctx.Err() != nil {
		ui.shouldExit = true
		return
	}
	ui.pressEnterToContinue()
}
