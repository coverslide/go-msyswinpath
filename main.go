package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

type stringMap map[string]string

const (
	VOLATILE_ENV = "HKCU\\Volatile Environment"
	USER_ENV     = "HKCU\\Environment"
	GLOBAL_ENV   = "HKLM\\System\\CurrentControlSet\\Control\\Session Manager\\Environment"
)

var (
	lineRegex            *regexp.Regexp = regexp.MustCompile("\\s+")
	winDriveLetterRegex  *regexp.Regexp = regexp.MustCompile("^([a-zA-Z]):")
	unixDriveLetterRegex *regexp.Regexp = regexp.MustCompile("^/([a-zA-Z])/")
	winVariableRegex     *regexp.Regexp = regexp.MustCompile("%([^%]+)%")
	duplicateSlashRegex  *regexp.Regexp = regexp.MustCompile("//+")
)

var (
	OK          = struct{}{}
	debug       = false
	existsCheck = false
)

func logDebug(format string, args ...interface{}) {
	if !debug {
		return
	}

	fmt.Fprintf(os.Stderr, format, args...)
}

func getRegOutput(path string) (string, error) {
	cmd := exec.Command("reg", "query", path)
	out, err := cmd.Output()
	return string(out), err
}

func parseRegOutput(output string) stringMap {
	regMap := make(stringMap)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		lineParts := lineRegex.Split(line, 4)
		if len(lineParts) < 4 {
			continue
		}
		key := lineParts[1]
		value := lineParts[3]

		regMap[strings.ToUpper(key)] = strings.TrimSpace(value)
	}

	return regMap
}

func getRegData(path string) (stringMap, error) {
	regOutput, err := getRegOutput(path)
	if err != nil {
		return nil, err
	}

	return parseRegOutput(regOutput), nil
}

func cleanupWinPath(volatileData stringMap, winPath string) string {
	fixedPath := strings.TrimRight(winPath, "\\")
	replaceVariableMatches := winVariableRegex.FindStringSubmatch(fixedPath)
	if len(replaceVariableMatches) > 0 {
		for _, variableName := range replaceVariableMatches[1:] {
			trueVariableName := strings.ToUpper(variableName)
			if value, ok := volatileData[trueVariableName]; ok {
				replaceStr := fmt.Sprintf("%%%s%%", variableName)
				fixedPath = strings.ReplaceAll(fixedPath, replaceStr, value)
			} else {
				logDebug("Unknown variable: %q\n", variableName)
				return ""
			}
		}
	}
	return fixedPath
}

func convertWinPathToUnix(winPath string) string {
	fixedPath := winPath
	driveLetterMatch := winDriveLetterRegex.FindStringSubmatch(fixedPath)
	if len(driveLetterMatch) == 0 {
		logDebug("No drive: %s\n", winPath)
	} else if len(driveLetterMatch) == 1 {
		logDebug("Could not extract drive letter: %s\n", winPath)
	} else {
		driveLetter := driveLetterMatch[1]
		trueDriveLetter := strings.ToLower(driveLetter)
		driveDirectory := fmt.Sprintf("/%s/", trueDriveLetter)
		fixedPath = winDriveLetterRegex.ReplaceAllString(fixedPath, driveDirectory)
	}

	fixedPath = strings.ReplaceAll(fixedPath, "\\", "/")
	fixedPath = duplicateSlashRegex.ReplaceAllString(fixedPath, "/")
	fixedPath = strings.TrimRight(fixedPath, "/")

	return fixedPath
}

func convertUnixPathToWin(unixPath string) string {
	fixedPath := unixPath
	fixedPath = unixDriveLetterRegex.ReplaceAllString(fixedPath, "$1:\\")
	fixedPath = strings.ReplaceAll(fixedPath, "/", "\\")
	return fixedPath
}

func winPathExists(winPath string) error {
	if !existsCheck {
		return nil
	}
	fh, err := os.Stat(winPath)
	if err != nil {
		return err
	}
	if !fh.IsDir() {
		return fmt.Errorf("Not a directory")
	}
	return nil
}

func extractPathData(envMaps ...stringMap) string {
	truePaths := make(map[string]struct{})

  combinedMaps := make(stringMap)
  for _, singleMap := range envMaps {
    for key, val := range singleMap {
      if oldVal, ok := combinedMaps[key] ; ok {
        logDebug("Key already set: %q : %q => %q\n", key, oldVal, val)
      }
      combinedMaps[key] = val
    }
  }

	var allPaths []string
	for _, envMap := range envMaps {
		if paths, ok := envMap["PATH"]; ok {
			total := 0
			pathsSlice := strings.Split(paths, ";")
			realPaths := make([]string, len(pathsSlice))
			for _, path := range pathsSlice {
				cleanPath := cleanupWinPath(combinedMaps, path)
				if cleanPath == "" {
					continue
				}
				if err := winPathExists(cleanPath); err != nil {
					logDebug("Invalid Path: %q, %s\n", cleanPath, err.Error())
					continue
				}
				unixPath := convertWinPathToUnix(cleanPath)
				truePath := strings.ToUpper(unixPath)
				if _, ok := truePaths[truePath]; !ok {
					truePaths[truePath] = OK
					realPaths[total] = unixPath
					total += 1
				} else {
					logDebug("Duplicate: %s\n", unixPath)
				}
			}
			allPaths = append(allPaths, realPaths[:total]...)
		}
	}

	return strings.Join(allPaths, ":")
}

func getUserPath() (string, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.READ)
	if err != nil {
		return "", fmt.Errorf("opening registry: %w", err)
	}
	defer key.Close()

	val, _, err := key.GetStringValue("PATH")
	if err != nil {
		if err == registry.ErrNotExist {
			return "", nil
		}
		return "", fmt.Errorf("reading PATH: %w", err)
	}
	return val, nil
}

func setUserPath(pathValue string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.WRITE)
	if err != nil {
		return fmt.Errorf("opening registry for write: %w", err)
	}
	defer key.Close()

	if pathValue == "" {
		if err := key.DeleteValue("PATH"); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("deleting PATH: %w", err)
		}
	} else {
		if err := key.SetStringValue("PATH", pathValue); err != nil {
			return fmt.Errorf("writing PATH: %w", err)
		}
	}

	broadcastEnvChange()
	return nil
}

func broadcastEnvChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	procSendMessageTimeout := user32.NewProc("SendMessageTimeoutW")

	const HWND_BROADCAST = uintptr(0xFFFF)
	const WM_SETTINGCHANGE = 0x001A
	const SMTO_BLOCK = 0x0001
	const timeout = 5000

	envStr, _ := syscall.UTF16PtrFromString("Environment")
	procSendMessageTimeout.Call(
		HWND_BROADCAST,
		WM_SETTINGCHANGE,
		0,
		uintptr(unsafe.Pointer(envStr)),
		SMTO_BLOCK,
		uintptr(timeout),
		0,
	)
}

func listPaths() error {
	rawPath, err := getUserPath()
	if err != nil {
		return err
	}
	if rawPath == "" {
		return nil
	}

	entries := strings.Split(rawPath, ";")
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed != "" {
			fmt.Println(trimmed)
		}
	}
	return nil
}

func addPathToRegistry(newPath string) error {
	rawPath, err := getUserPath()
	if err != nil {
		return err
	}

	newPath = strings.TrimSpace(newPath)
	if newPath == "" {
		return fmt.Errorf("path cannot be empty")
	}

	var entries []string
	if rawPath != "" {
		entries = strings.Split(rawPath, ";")
	}

	normalizedNew := strings.ToUpper(strings.TrimRight(newPath, "\\"))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		normalized := strings.ToUpper(strings.TrimRight(trimmed, "\\"))
		if normalized == normalizedNew {
			return nil
		}
	}

	entries = append(entries, newPath)
	newValue := strings.Join(entries, ";")
	return setUserPath(newValue)
}

func removePathFromRegistry(targetPath string) error {
	rawPath, err := getUserPath()
	if err != nil {
		return err
	}

	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		return fmt.Errorf("path cannot be empty")
	}

	if rawPath == "" {
		return nil
	}

	entries := strings.Split(rawPath, ";")
	normalizedTarget := strings.ToUpper(strings.TrimRight(targetPath, "\\"))

	var remaining []string
	found := false
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		normalized := strings.ToUpper(strings.TrimRight(trimmed, "\\"))
		if normalized == normalizedTarget {
			found = true
			continue
		}
		remaining = append(remaining, trimmed)
	}

	if !found {
		return nil
	}

	newValue := strings.Join(remaining, ";")
	return setUserPath(newValue)
}

func main() {
	if runtime.GOOS != "windows" {
		fmt.Fprintf(os.Stderr, "This tool was meant to be run on windows")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printMSYSPath()
		return
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "add":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s add <path>\n", os.Args[0])
			os.Exit(1)
		}
		if err := addPathToRegistry(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "remove":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s remove <path>\n", os.Args[0])
			os.Exit(1)
		}
		if err := removePathFromRegistry(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := listPaths(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subcommand)
		fmt.Fprintf(os.Stderr, "Usage: %s [add|remove|list] [path]\n", os.Args[0])
		os.Exit(1)
	}
}

func printMSYSPath() {
	debugPtr := flag.Bool("debug", false, "Enable debug mode")
	flag.BoolVar(debugPtr, "d", false, "Enable debug mode")
	existencePtr := flag.Bool("exists", false, "Check if directory exists")
	flag.BoolVar(existencePtr, "e", false, "Check if directory exists")

	flag.Parse()

	debug = *debugPtr
	existsCheck = *existencePtr

	volatileData, err := getRegData(VOLATILE_ENV)
	if err != nil {
		panic(err)
	}
	userData, err := getRegData(USER_ENV)
	if err != nil {
		panic(err)
	}
	globalData, err := getRegData(GLOBAL_ENV)
	if err != nil {
		panic(err)
	}
	paths := extractPathData(volatileData, userData, globalData)
	fmt.Println(paths)
}
