package main

import (
	"fmt"
	"os"
	"os/exec"
  "strings"
  "regexp"
  "runtime"
)

type stringMap map[string]string

const (
	VOLATILE_ENV = "HKCU\\Volatile Environment"
	USER_ENV     = "HKCU\\Environment"
	GLOBAL_ENV   = "HKLM\\System\\CurrentControlSet\\Control\\Session Manager\\Environment"
)

var lineRegex *regexp.Regexp = regexp.MustCompile("\\s+")
var winDriveLetterRegex *regexp.Regexp = regexp.MustCompile("^([a-zA-Z]):")
var unixDriveLetterRegex *regexp.Regexp = regexp.MustCompile("^/([a-zA-Z])/")
var winVariableRegex *regexp.Regexp = regexp.MustCompile("%([^%]+)%")
var duplicateSlashRegex *regexp.Regexp = regexp.MustCompile("//+")

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

func convertWinPathToUnix(volatileData stringMap, winPath string) string {
  fixedPath := winPath
  replaceVariableMatches := winVariableRegex.FindStringSubmatch(fixedPath)
  if len(replaceVariableMatches) > 0 {
    for _, variableName := range replaceVariableMatches[1:] {
      trueVariableName := strings.ToUpper(variableName)
      if value, ok := volatileData[trueVariableName] ; ok {
        replaceStr := fmt.Sprintf("%%%s%%", variableName)
        fixedPath = strings.ReplaceAll(fixedPath, replaceStr, value)
      } else {
        // fmt.Fprintf(os.Stderr, "Unknown variable: %q\n", variableName)
      }
    }
  }

  driveLetterMatch := winDriveLetterRegex.FindStringSubmatch(fixedPath)
  if len(driveLetterMatch) == 0 {
    // fmt.Fprintf(os.Stderr, "No drive: %s\n", winPath)
  } else if len(driveLetterMatch) == 1 {
    // fmt.Fprintf(os.Stderr, "Could not extract drive letter: %s\n", winPath)
  } else {
    driveLetter := driveLetterMatch[1]
    // msys uses lower case drive letter
    trueDriveLetter := strings.ToLower(driveLetter)
    driveDirectory := fmt.Sprintf("/%s/", trueDriveLetter)
    fixedPath = winDriveLetterRegex.ReplaceAllString(fixedPath, driveDirectory)
  }

  fixedPath = strings.ReplaceAll(fixedPath, "\\", "/")
  fixedPath = duplicateSlashRegex.ReplaceAllString(fixedPath, "/")

  return fixedPath
}

func convertUnixPathToWin(unixPath string) string {
  fixedPath := unixPath
  fixedPath = unixDriveLetterRegex.ReplaceAllString(fixedPath, "$1:\\")
  fixedPath = strings.ReplaceAll(fixedPath, "/", "\\")
  return fixedPath
}

func validatePath(unixPath string) error {
  winPath := convertUnixPathToWin(unixPath)
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
  truePaths := make(stringMap)

  for _, envMap := range envMaps {
    if paths, ok := envMap["PATH"] ; ok {
      pathsSlice := strings.Split(paths, ";")
      for _, path := range pathsSlice {
        unixPath := convertWinPathToUnix(volatileData, path)
        if err := validatePath(unixPath) ; err != nil {
          // fmt.Fprintf(os.Stderr, "Invalid Path: %q, %e\n", unixPath, err.Error())
        } else {
          truePath := strings.ToUpper(unixPath)
          if _, ok := truePaths[truePath] ; !ok {
            truePaths[truePath] = unixPath
          } else {
            // fmt.Fprintf(os.Stderr, "Duplicate: %s\n", unixPath)
          }
        }
      }
    }
  }

  realPaths := make([]string, len(truePaths))

  index := 0
  for _, value := range truePaths {
    realPaths[index] = value
    index++
  }

  return strings.Join(realPaths, ":")
}

func main() {
  if runtime.GOOS != "windows" {
    fmt.Fprintf(os.Stderr, "This tool was meant to be run on windows")
    os.Exit(1)
  }

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
  fmt.Print(paths)
}

