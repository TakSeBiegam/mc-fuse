package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	var unresolved []string

	for {
		start := strings.Index(s, "${")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end == -1 {
			break
		}
		end += start

		key := s[start+2 : end]
		if val, ok := secrets[key]; ok {
			s = s[:start] + val + s[end+1:]
		} else {
			unresolved = append(unresolved, key)
			s = s[:start] + "\x00UNRESOLVED:" + key + "\x00" + s[end+1:]
		}
	}

	for _, key := range unresolved {
		s = strings.ReplaceAll(s, "\x00UNRESOLVED:"+key+"\x00", "${"+key+"}")
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
