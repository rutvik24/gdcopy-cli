package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type volumeInfo struct {
	Name string
	Path string
}

func listMountedVolumes() []volumeInfo {
	switch runtime.GOOS {
	case "darwin":
		return listVolumesFromDir("/Volumes")
	case "linux":
		var volumes []volumeInfo
		volumes = append(volumes, listVolumesFromDir("/mnt")...)
		volumes = append(volumes, listLinuxMediaVolumes()...)
		return volumes
	case "windows":
		return listWindowsVolumes()
	default:
		return nil
	}
}

func listVolumesFromDir(root string) []volumeInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var volumes []volumeInfo
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(root, name)
		if !isNavigableDir(fullPath) {
			continue
		}
		volumes = append(volumes, volumeInfo{Name: name, Path: fullPath})
	}
	return volumes
}

func listLinuxMediaVolumes() []volumeInfo {
	entries, err := os.ReadDir("/media")
	if err != nil {
		return nil
	}

	var volumes []volumeInfo
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		userMediaPath := filepath.Join("/media", name)
		if !isNavigableDir(userMediaPath) {
			continue
		}

		subEntries, err := os.ReadDir(userMediaPath)
		if err != nil {
			volumes = append(volumes, volumeInfo{Name: name, Path: userMediaPath})
			continue
		}

		foundSubdir := false
		for _, sub := range subEntries {
			subName := sub.Name()
			if strings.HasPrefix(subName, ".") {
				continue
			}
			subPath := filepath.Join(userMediaPath, subName)
			if !isNavigableDir(subPath) {
				continue
			}
			foundSubdir = true
			volumes = append(volumes, volumeInfo{Name: subName, Path: subPath})
		}
		if !foundSubdir {
			volumes = append(volumes, volumeInfo{Name: name, Path: userMediaPath})
		}
	}
	return volumes
}

func listWindowsVolumes() []volumeInfo {
	var volumes []volumeInfo
	for letter := 'A'; letter <= 'Z'; letter++ {
		path := string(letter) + ":\\"
		if isNavigableDir(path) {
			volumes = append(volumes, volumeInfo{Name: string(letter) + ":", Path: path})
		}
	}
	return volumes
}

func isNavigableDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
