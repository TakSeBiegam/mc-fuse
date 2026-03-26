package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type serverKind string

const (
	serverKindPaper    serverKind = "paper"
	serverKindVelocity serverKind = "velocity"
)

func findServerJAR(serverDir string) (string, error) {
	velocityJars, _ := filepath.Glob(filepath.Join(serverDir, "velocity-*.jar"))
	if len(velocityJars) > 0 {
		sort.Strings(velocityJars)
		return filepath.Base(velocityJars[len(velocityJars)-1]), nil
	}

	paperJars, _ := filepath.Glob(filepath.Join(serverDir, "paper-*.jar"))
	if len(paperJars) > 0 {
		sort.Strings(paperJars)
		return filepath.Base(paperJars[len(paperJars)-1]), nil
	}

	if _, err := os.Stat(filepath.Join(serverDir, "server.jar")); err == nil {
		return "server.jar", nil
	}

	allJars, _ := filepath.Glob(filepath.Join(serverDir, "*.jar"))
	if len(allJars) > 0 {
		return filepath.Base(allJars[0]), nil
	}

	return "", fmt.Errorf("no JAR file found in %s", serverDir)
}

func detectServerKind(serverDir, jarName string) serverKind {
	if strings.HasPrefix(strings.ToLower(jarName), "velocity-") {
		return serverKindVelocity
	}

	if _, err := os.Stat(filepath.Join(serverDir, "velocity.toml")); err == nil {
		return serverKindVelocity
	}

	return serverKindPaper
}

func defaultJavaOpts(kind serverKind, minRam, maxRam string) string {
	if kind == serverKindVelocity {
		return fmt.Sprintf("-Xms%s -Xmx%s -XX:+UseG1GC -XX:G1HeapRegionSize=4M -XX:+UnlockExperimentalVMOptions -XX:+ParallelRefProcEnabled -XX:+AlwaysPreTouch -XX:MaxInlineLevel=15",
			minRam, maxRam)
	}

	return fmt.Sprintf("-Xms%s -Xmx%s -XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200",
		minRam, maxRam)
}

func buildLaunchArgs(kind serverKind, jarName, javaOpts string) []string {
	args := []string{}
	for _, opt := range splitArgs(javaOpts) {
		args = append(args, opt)
	}
	args = append(args, "-jar", jarName)
	if kind == serverKindPaper {
		args = append(args, "--nogui")
	}
	return args
}

func launchServer(mountDir string, kind serverKind, jarName, javaOpts string) (*exec.Cmd, error) {
	javaPath, err := exec.LookPath("java")
	if err != nil {
		return nil, fmt.Errorf("java not found in PATH: %w", err)
	}

	args := buildLaunchArgs(kind, jarName, javaOpts)

	cmd := exec.Command(javaPath, args...)
	cmd.Dir = mountDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
		"TERM=" + os.Getenv("TERM"),
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start server: %w", err)
	}

	return cmd, nil
}

func splitArgs(s string) []string {
	var args []string
	var current []byte
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else {
				current = append(current, c)
			}
		} else if c == '"' || c == '\'' {
			inQuote = true
			quoteChar = c
		} else if c == ' ' || c == '\t' {
			if len(current) > 0 {
				args = append(args, string(current))
				current = current[:0]
			}
		} else {
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		args = append(args, string(current))
	}
	return args
}
