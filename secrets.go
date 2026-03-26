package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var placeholderTokenRe = regexp.MustCompile(`\$\{([^{}]+)\}`)

func loadSecrets(sopsFile string) (map[string]string, error) {
	if _, err := os.Stat(sopsFile); err != nil {
		return nil, fmt.Errorf("secrets file not found: %s", sopsFile)
	}

	sopsPath, err := exec.LookPath("sops")
	if err != nil {
		return nil, fmt.Errorf("sops binary not found in PATH: %w", err)
	}

	cmd := exec.Command(sopsPath, "--decrypt", sopsFile)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops decrypt failed: %w", err)
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse decrypted YAML: %w", err)
	}

	secrets := make(map[string]string, len(raw))
	for k, v := range raw {
		secrets[k] = fmt.Sprintf("%v", v)
	}

	return secrets, nil
}

func buildReverseMap(secrets map[string]string) map[string]string {
	rev := make(map[string]string, len(secrets))
	for k, v := range secrets {
		if len(v) >= 2 {
			rev[v] = "${" + k + "}"
		}
	}
	return rev
}

func substituteSecrets(data []byte, secrets map[string]string) ([]byte, []string) {
	s := string(data)
	unresolvedSet := make(map[string]struct{})

	const maxPasses = 128
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		s = placeholderTokenRe.ReplaceAllStringFunc(s, func(token string) string {
			key := token[2 : len(token)-1]
			val, ok := secrets[key]
			if !ok {
				unresolvedSet[key] = struct{}{}
				return token
			}
			if val != token {
				changed = true
			}
			return val
		})

		if !changed {
			break
		}
	}

	unresolved := make([]string, 0, len(unresolvedSet))
	for key := range unresolvedSet {
		unresolved = append(unresolved, key)
	}

	return []byte(s), unresolved
}

func reverseSubstitute(data []byte, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return data
	}

	s := string(data)

	type kv struct {
		plain  string
		marker string
	}
	pairs := make([]kv, 0, len(reverseMap))
	for plain, marker := range reverseMap {
		pairs = append(pairs, kv{plain, marker})
	}
	// Longest first.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && len(pairs[j].plain) > len(pairs[j-1].plain); j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}

	for _, p := range pairs {
		s = strings.ReplaceAll(s, p.plain, p.marker)
	}

	return []byte(s)
}
