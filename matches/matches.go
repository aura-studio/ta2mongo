package matches

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
)

func regexMetaIndex(s string) int {
	// Basic set of regex meta characters.
	// Used to derive a reasonable WalkDir base directory from the pattern.
	const metas = "^$.*+?()[]{}|\\"
	for i, r := range s {
		if strings.ContainsRune(metas, r) {
			return i
		}
	}
	return -1
}

func regexBaseDir(pattern string) string {
	if pattern == "" {
		return "."
	}

	// Take the prefix before the first meta char.
	idx := regexMetaIndex(pattern)
	prefix := pattern
	if idx >= 0 {
		prefix = pattern[:idx]
	}

	prefix = strings.TrimRight(prefix, "/\\")
	if prefix == "" {
		return string(filepath.Separator)
	}

	// If prefix ends with a separator we want that directory; otherwise take its parent.
	if strings.HasSuffix(pattern, string(filepath.Separator)) {
		return prefix
	}
	return filepath.Dir(prefix)
}

// CollectMatches treats each item in patterns as a REGEX matched against file paths.
// It walks from a derived base directory and filters candidates.
// Returned paths are absolute (as discovered by WalkDir) and deduplicated.
func CollectMatches(patterns []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 1024)

	logger := logrus.StandardLogger()

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			logger.WithError(err).WithField("regex", pattern).Warn("[ta2mongo] invalid logPattern regex; skipped")
			continue
		}

		baseDir := regexBaseDir(pattern)

		_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				logger.WithError(err).WithField("path", path).Warn("[ta2mongo] WalkDir error; continuing")
				return nil
			}
			if d == nil || d.IsDir() {
				return nil
			}
			if re.MatchString(path) {
				if _, ok := seen[path]; ok {
					return nil
				}
				seen[path] = struct{}{}
				out = append(out, path)
			}
			return nil
		})
	}

	return out
}
