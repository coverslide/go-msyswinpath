package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
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

func extractPathData(volatileData stringMap, envMaps ...stringMap) string {
	truePaths := make(map[string]struct{})

	var allPaths []string
	for _, envMap := range envMaps {
		if paths, ok := envMap["PATH"]; ok {
			total := 0
			pathsSlice := strings.Split(paths, ";")
			realPaths := make([]string, len(pathsSlice))
			for _, path := range pathsSlice {
				cleanPath := cleanupWinPath(volatileData, path)
				if cleanPath == "" {
					continue
				}
				if err := winPathExists(cleanPath); err != nil {
					logDebug("Invalid Path: %q, %e\n", cleanPath, err.Error())
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

func main() {
	if runtime.GOOS != "windows" {
		fmt.Fprintf(os.Stderr, "This tool was meant to be run on windows")
		os.Exit(1)
	}

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
