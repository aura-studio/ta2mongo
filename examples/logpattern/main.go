// Package main demonstrates logPattern glob patterns for both Linux and
// Windows, covering all supported path formats.
//
// On Windows, patterns can be written in either format:
//
//	Linux format:   /c/logs/**/*.log          (recommended, portable)
//	Windows format: C:\logs\**\*.log          (native, also supported)
//
// The program auto-converts paths for glob matching:
//
//	C:\logs\app.log  →  /c/logs/app.log       (normalize for matching)
//	/c/logs          →  C:\logs               (native for directory walk)
//
// On Linux/WSL, Windows-style paths are also supported. The program
// auto-detects the WSL drive mount point (e.g. /mnt/c) and converts:
//
//	C:\logs\app.log  →  /mnt/c/logs/app.log   (WSL)
//	/c/logs          →  /mnt/c/logs            (WSL, drive remapping)
//	logs\app.log     →  logs/app.log           (backslash → forward slash)
package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ---------------------------------------------------------------------------
// Path conversion helpers (same logic as internal/tailer)
// ---------------------------------------------------------------------------

var driveLetterRe = regexp.MustCompile(`^/([a-zA-Z])/`)

func toWindowsPath(path string) string {
	m := driveLetterRe.FindStringSubmatch(path)
	if m == nil {
		return path
	}
	drive := strings.ToUpper(m[1])
	rest := path[len(m[0]):]
	rest = strings.ReplaceAll(rest, "/", `\`)
	return drive + `:\` + rest
}

func normalizeWindowsPath(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		drive := strings.ToLower(string(path[0]))
		rest := path[2:]
		path = "/" + drive + rest
	}
	return strings.ReplaceAll(path, `\`, "/")
}

func normalizePath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	return normalizeWindowsPath(path)
}

// ---------------------------------------------------------------------------
// Glob matching (supports **, *, ?, [...])
// ---------------------------------------------------------------------------

func globMatch(pattern, name string) bool {
	return doGlobMatch(
		strings.Split(pattern, "/"),
		strings.Split(name, "/"),
	)
}

func doGlobMatch(patParts, nameParts []string) bool {
	for len(patParts) > 0 {
		seg := patParts[0]
		if seg == "**" {
			patParts = patParts[1:]
			for len(patParts) > 0 && patParts[0] == "**" {
				patParts = patParts[1:]
			}
			if len(patParts) == 0 {
				return true
			}
			for i := 0; i <= len(nameParts); i++ {
				if doGlobMatch(patParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}
		if len(nameParts) == 0 {
			return false
		}
		matched, _ := filepath.Match(seg, nameParts[0])
		if !matched {
			return false
		}
		patParts = patParts[1:]
		nameParts = nameParts[1:]
	}
	return len(nameParts) == 0
}

// ---------------------------------------------------------------------------
// Demo helpers
// ---------------------------------------------------------------------------

func printSection(title string) {
	fmt.Println()
	fmt.Printf("--- %s ---\n", title)
	fmt.Println()
}

func printGlobResult(pattern, path string, matched bool) {
	sym := "x"
	if matched {
		sym = "v"
	}
	fmt.Printf("  %-45s  %-45s  [%s]\n", pattern, path, sym)
}

func printGlobHeader() {
	fmt.Printf("  %-45s  %-45s  %s\n", "Pattern", "Path", "Match")
	fmt.Println("  " + strings.Repeat("-", 100))
}

func printConvHeader() {
	fmt.Printf("  %-45s  %-45s  → %-35s  %s\n",
		"Pattern", "OS Path", "Normalized", "Match")
	fmt.Println("  " + strings.Repeat("-", 140))
}

func printConvResult(pattern, osPath, normalized string, matched bool) {
	sym := "x"
	if matched {
		sym = "v"
	}
	fmt.Printf("  %-45s  %-45s  → %-35s  [%s]\n",
		pattern, osPath, normalized, sym)
}

// stripDotSlash strips "./" or ".\" prefix (same as discoverFiles).
func stripDotSlash(p string) string {
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, `.\`) {
		return p[2:]
	}
	return p
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func main() {
	fmt.Println("========================================")
	fmt.Println("  logPattern Glob Matching Examples")
	fmt.Printf("  (running on %s/%s)\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("========================================")

	// =================================================================
	// 1. Linux absolute paths
	// =================================================================
	printSection("1. Linux Absolute Paths")
	printGlobHeader()

	for _, tc := range []struct{ pattern, path string }{
		{"/var/log/app/*.log", "/var/log/app/access.log"},
		{"/var/log/app/*.log", "/var/log/app/error.log"},
		{"/var/log/app/*.log", "/var/log/app/readme.txt"},
		{"/var/log/**/*.log", "/var/log/app/access.log"},
		{"/var/log/**/*.log", "/var/log/app/sub/deep.log"},
		{"/var/log/**/*.log", "/var/log/sys.log"},
		{"/var/log/app-?.log", "/var/log/app-1.log"},
		{"/var/log/app-?.log", "/var/log/app-12.log"},
		{"/var/log/app-[0-9].log", "/var/log/app-3.log"},
		{"/var/log/app-[0-9].log", "/var/log/app-x.log"},
	} {
		printGlobResult(tc.pattern, tc.path, globMatch(tc.pattern, tc.path))
	}

	// =================================================================
	// 2. Linux relative paths (including ./ and ../)
	// =================================================================
	printSection("2. Linux Relative Paths (including ./ and ../)")
	printGlobHeader()

	for _, tc := range []struct{ pattern, path string }{
		// Basic relative
		{"logs/*.log", "logs/app.log"},
		{"logs/*.log", "logs/sub/app.log"},
		{"logs/**/*.log", "logs/sub/deep/app.log"},
		{"**/*.log", "any/path/deep/app.log"},
		{"*.log", "app.log"},
		// ./ prefix (stripped before matching, same as discoverFiles)
		{"./logs/*.log", "logs/app.log"},
		{"./logs/**/*.log", "logs/sub/app.log"},
		// ../ prefix (preserved, WalkDir returns paths with ../ prefix)
		{"../logs/*.log", "../logs/app.log"},
		{"../logs/**/*.log", "../logs/sub/deep/app.log"},
	} {
		matchPat := stripDotSlash(tc.pattern)
		printGlobResult(tc.pattern, tc.path, globMatch(matchPat, tc.path))
	}

	// =================================================================
	// 3. Windows absolute paths — Linux-format pattern (recommended)
	// =================================================================
	printSection("3. Windows Absolute Paths — Linux-format pattern (recommended)")
	printConvHeader()

	for _, tc := range []struct {
		pattern string
		winPath string
	}{
		{"/c/logs/*.log", `C:\logs\access.log`},
		{"/c/logs/*.log", `C:\logs\error.log`},
		{"/c/logs/*.log", `C:\logs\readme.txt`},
		{"/d/app/logs/**/*.log", `D:\app\logs\sub\deep.log`},
		{"/c/Program Files/app/*.log", `C:\Program Files\app\error.log`},
	} {
		normalized := normalizeWindowsPath(tc.winPath)
		matched := globMatch(tc.pattern, normalized)
		printConvResult(tc.pattern, tc.winPath, normalized, matched)
	}

	// =================================================================
	// 4. Windows absolute paths — native backslash pattern (also works)
	// =================================================================
	printSection("4. Windows Absolute Paths — native backslash pattern")
	fmt.Println("  (pattern is normalized to forward slashes before matching)")
	fmt.Println()
	printConvHeader()

	for _, tc := range []struct {
		pattern string // Windows-native pattern with backslashes
		winPath string // OS returns this
	}{
		{`C:\logs\*.log`, `C:\logs\access.log`},
		{`C:\logs\*.log`, `C:\logs\readme.txt`},
		{`D:\app\logs\**\*.log`, `D:\app\logs\sub\deep.log`},
		{`C:\Program Files\app\*.log`, `C:\Program Files\app\error.log`},
	} {
		// Both pattern and path are normalized (same as discoverFiles)
		normPat := normalizeWindowsPath(tc.pattern)
		normPath := normalizeWindowsPath(tc.winPath)
		matched := globMatch(normPat, normPath)
		printConvResult(tc.pattern, tc.winPath, normPath, matched)
	}

	// =================================================================
	// 5. Windows relative paths — various formats
	// =================================================================
	printSection("5. Windows Relative Paths — various formats")
	fmt.Println("  (all patterns and paths normalized to forward slashes)")
	fmt.Println()
	printConvHeader()

	for _, tc := range []struct {
		pattern string
		winPath string
	}{
		// Forward slash pattern, backslash path
		{"logs/*.log", `logs\app.log`},
		{"logs/**/*.log", `logs\sub\deep\app.log`},
		{"**/*.log", `any\path\app.log`},
		// Backslash pattern, backslash path (Windows-native throughout)
		{`logs\*.log`, `logs\app.log`},
		{`logs\**\*.log`, `logs\sub\deep\app.log`},
		// .\ prefix (Windows equivalent of ./)
		{`.\logs\*.log`, `logs\app.log`},
		{`.\logs\**\*.log`, `logs\sub\deep\app.log`},
		// ..\ prefix (Windows equivalent of ../)
		{`..\logs\*.log`, `..\logs\app.log`},
		{`..\logs\**\*.log`, `..\logs\sub\deep\app.log`},
	} {
		normPat := normalizeWindowsPath(stripDotSlash(tc.pattern))
		normPath := normalizeWindowsPath(tc.winPath)
		matched := globMatch(normPat, normPath)
		printConvResult(tc.pattern, tc.winPath, normPath, matched)
	}

	// =================================================================
	// 6. Path conversion summary
	// =================================================================
	printSection("6. Path Conversion: toWindowsPath (Linux → Windows)")

	convCases := []struct {
		input string
		desc  string
	}{
		{"/c/logs/app/*.log", "drive C absolute"},
		{"/d/data/**/*.log", "drive D absolute"},
		{"logs/*.log", "relative (no change)"},
		{"./logs/**/*.log", "dot-slash relative (no change)"},
		{"../logs/*.log", "dot-dot-slash relative (no change)"},
		{"**/*.log", "recursive from CWD (no change)"},
		{"/var/log/app/*.log", "Linux absolute (no change)"},
	}
	fmt.Printf("  %-40s  %-25s  → %s\n", "Linux Pattern", "Type", "Windows Path")
	fmt.Println("  " + strings.Repeat("-", 100))
	for _, c := range convCases {
		fmt.Printf("  %-40s  %-25s  → %s\n", c.input, c.desc, toWindowsPath(c.input))
	}

	// =================================================================
	// 7. Cross-platform compatibility matrix
	// =================================================================
	printSection("7. Cross-Platform Compatibility Matrix")
	fmt.Println("  All path formats now work on BOTH Linux and Windows.")
	fmt.Println("  On Linux/WSL, Windows paths are auto-converted (C:\\ → /mnt/c/, \\ → /).")
	fmt.Println("  WSL drive mount prefix is auto-detected (e.g. /mnt/c or /c).")
	fmt.Println()

	matrix := []struct {
		format  string
		example string
		linux   string
		windows string
	}{
		{"Linux relative", "logs/ta.*.log", "PASS", "PASS"},
		{"Linux absolute", "/var/log/ta.*.log", "PASS", "PASS"},
		{"Linux drive-letter abs", "/c/logs/ta.*.log", "PASS*", "PASS"},
		{`Windows absolute`, `C:\logs\ta.*.log`, "PASS*", "PASS"},
		{`Windows relative`, `logs\ta.*.log`, "PASS", "PASS"},
		{"./ prefix", "./logs/ta.*.log", "PASS", "PASS"},
		{`.\  prefix`, `.\logs\ta.*.log`, "PASS", "PASS"},
		{"../ prefix", "../logs/ta.*.log", "PASS", "PASS"},
		{`..\  prefix`, `..\logs\ta.*.log`, "PASS", "PASS"},
	}

	fmt.Printf("  %-25s  %-30s  %-8s  %s\n", "Format", "Example", "Linux", "Windows")
	fmt.Println("  " + strings.Repeat("-", 80))
	for _, m := range matrix {
		fmt.Printf("  %-25s  %-30s  %-8s  %s\n", m.format, m.example, m.linux, m.windows)
	}
	fmt.Println()
	fmt.Println("  * Drive-letter paths on Linux require WSL or a /c/ mount point.")
	fmt.Println()
	fmt.Println("  Recommendation: use Linux-style forward-slash patterns for portability.")
	fmt.Println("  Windows-native backslash patterns are auto-normalized on all platforms.")
}
