package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/manifoldco/promptui"
)

// SelectLocalPath starts an interactive local file explorer to choose a directory or file.
// In DB mode, it selects a single database file. In upload mode, it supports multi-selection of files/folders.
func SelectLocalPath(startPath string, dbMode bool) ([]string, error) {
	currentDir, err := filepath.Abs(startPath)
	if err != nil {
		currentDir, _ = filepath.Abs(".")
	}

	selectedPaths := make(map[string]bool)
	cursorPos := 0

	for {
		// Read current directory contents
		entries, err := os.ReadDir(currentDir)
		if err != nil {
			fmt.Printf("Error reading directory %s: %v\n", currentDir, err)
			parent := filepath.Dir(currentDir)
			if parent != currentDir {
				currentDir = parent
				cursorPos = 0
				continue
			}
			return nil, err
		}

		var subdirs []os.DirEntry
		var files []os.DirEntry

		for _, entry := range entries {
			if dirEntryIsDir(entry, currentDir) {
				subdirs = append(subdirs, entry)
			} else if !entry.IsDir() {
				files = append(files, entry)
			}
		}

		type Option struct {
			Label  string
			Action func() ([]string, bool, error)
		}

		var options []Option

		// 1. Action options at the top
		if dbMode {
			options = append(options, Option{
				Label: fmt.Sprintf("[Create/select new .db file in: %s]", currentDir),
				Action: func() ([]string, bool, error) {
					promptInput := promptui.Prompt{
						Label:   "Enter the name for the new SQLite database file (must end in .db)",
						Default: "gdcopy_uploads.db",
						Validate: func(input string) error {
							trimmed := strings.TrimSpace(input)
							if trimmed == "" {
								return fmt.Errorf("filename cannot be empty")
							}
							if filepath.Ext(trimmed) != ".db" {
								return fmt.Errorf("file extension must be .db")
							}
							return nil
						},
					}
					name, err := promptInput.Run()
					if err != nil {
						return nil, false, nil // return to navigation
					}
					return []string{filepath.Join(currentDir, strings.TrimSpace(name))}, true, nil
				},
			})
		} else {
			// Upload mode: Confirmation option showing total selected items count
			selCount := 0
			for _, sel := range selectedPaths {
				if sel {
					selCount++
				}
			}
			options = append(options, Option{
				Label: fmt.Sprintf("[Confirm Selection (%d items selected)]", selCount),
				Action: func() ([]string, bool, error) {
					var result []string
					for path, sel := range selectedPaths {
						if sel {
							result = append(result, path)
						}
					}
					if len(result) == 0 {
						fmt.Println("No items selected. Please select at least one item using [ ] checkboxes.")
						return nil, false, nil
					}
					return result, true, nil
				},
			})
		}

		// 2. Quick-jump navigation
		if home, err := os.UserHomeDir(); err == nil && filepath.Clean(currentDir) != filepath.Clean(home) {
			homePath := home
			options = append(options, Option{
				Label: "🏠 [Go to Home]",
				Action: func() ([]string, bool, error) {
					currentDir = homePath
					cursorPos = 0
					return nil, false, nil
				},
			})
		}

		for _, vol := range listMountedVolumes() {
			if filepath.Clean(currentDir) == filepath.Clean(vol.Path) {
				continue
			}
			volPath := vol.Path
			volName := vol.Name
			options = append(options, Option{
				Label: fmt.Sprintf("💿 [External Drive: %s]", volName),
				Action: func() ([]string, bool, error) {
					currentDir = volPath
					cursorPos = 0
					return nil, false, nil
				},
			})
		}

		// 3. Parent navigation
		parentDir := filepath.Dir(currentDir)
		if parentDir != currentDir {
			options = append(options, Option{
				Label: ".. (Go Up)",
				Action: func() ([]string, bool, error) {
					currentDir = parentDir
					cursorPos = 0
					return nil, false, nil
				},
			})
		}

		options = append(options, Option{
			Label: "[Cancel]",
			Action: func() ([]string, bool, error) {
				return nil, true, fmt.Errorf("cancelled by user")
			},
		})

		// Helper to check if a path is selected
		isSel := func(path string) string {
			if selectedPaths[path] {
				return "[\u2713]" // Checkmark: ✓
			}
			return "[ ]"
		}

		// 4. Subdirectories
		for _, sd := range subdirs {
			name := sd.Name()
			fullPath := filepath.Join(currentDir, name)
			
			if dbMode {
				// In DB mode, clicking a directory simply navigates into it
				options = append(options, Option{
					Label: "📁 " + name + "/",
					Action: func() ([]string, bool, error) {
						currentDir = fullPath
						cursorPos = 0
						return nil, false, nil
					},
				})
			} else {
				// In upload/multi-select mode, we can toggle selection or navigate in
				optFullPath := fullPath // copy for closure
				options = append(options, Option{
					Label: fmt.Sprintf("%s 📁 %s/ (Toggle Select)", isSel(optFullPath), name),
					Action: func() ([]string, bool, error) {
						selectedPaths[optFullPath] = !selectedPaths[optFullPath]
						return nil, false, nil
					},
				})
				options = append(options, Option{
					Label: "     Go into: " + name + "/",
					Action: func() ([]string, bool, error) {
						currentDir = optFullPath
						cursorPos = 0
						return nil, false, nil
					},
				})
			}
		}

		// 5. Files
		for _, f := range files {
			name := f.Name()
			fullPath := filepath.Join(currentDir, name)
			optFullPath := fullPath // copy for closure

			if dbMode {
				if filepath.Ext(name) == ".db" {
					options = append(options, Option{
						Label: "💾 " + name,
						Action: func() ([]string, bool, error) {
							return []string{optFullPath}, true, nil
						},
					})
				}
			} else {
				options = append(options, Option{
					Label: fmt.Sprintf("%s 📄 %s", isSel(optFullPath), name),
					Action: func() ([]string, bool, error) {
						selectedPaths[optFullPath] = !selectedPaths[optFullPath]
						return nil, false, nil
					},
				})
			}
		}

		var labels []string
		for _, opt := range options {
			labels = append(labels, opt.Label)
		}

		var header string
		if dbMode {
			header = fmt.Sprintf("\nLocal Directory: %s\nSelect a SQLite database file:", currentDir)
		} else {
			header = fmt.Sprintf("\nLocal Directory: %s\nSelect files/folders to upload (type to search):", currentDir)
		}

		searcher := func(input string, index int) bool {
			return strings.Contains(strings.ToLower(labels[index]), strings.ToLower(input))
		}

		prompt := promptui.Select{
			Label:     header,
			Items:     labels,
			Size:      15,
			Searcher:  searcher,
			CursorPos: cursorPos,
			Templates: &promptui.SelectTemplates{
				Label:    "{{ . }}",
				Active:   "\U0001F449 {{ . | cyan }}",
				Inactive: "   {{ . }}",
				Selected: "\U0001F449 {{ . | green | bold }}",
			},
		}

		idx, _, err := prompt.Run()
		if err != nil {
			if err == promptui.ErrInterrupt {
				return nil, fmt.Errorf("interrupted")
			}
			return nil, err
		}

		// Save the cursor position to keep it in place on checkbox toggle
		cursorPos = idx

		selectedPathsResult, exit, err := options[idx].Action()
		if err != nil {
			return nil, err
		}
		if exit {
			return selectedPathsResult, nil
		}
	}
}

func dirEntryIsDir(entry os.DirEntry, parent string) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}
