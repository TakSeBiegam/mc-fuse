package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func validateSecrets(sourceDir string, secrets map[string]string) []string {
	var errors []string

	filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !isTextConfig(path) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			matches := placeholderRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				key := m[1]
				if _, ok := secrets[key]; !ok {
					rel, _ := filepath.Rel(sourceDir, path)
					errors = append(errors, fmt.Sprintf("  %s:%d → brakuje sekretu: ${%s}", rel, lineNum+1, key))
				}
			}
		}
		return nil
	})

	return errors
}
