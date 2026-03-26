package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const version = "1.1.0"

func main() {
	log.SetPrefix("[mc-fuse] ")
	log.SetFlags(0)

	secretsFile := flag.String("secrets", "", "Path to SOPS-encrypted secrets file (required)")
	valuesFile := flag.String("values", "", "Path to SOPS-encrypted global values file (loaded before --secrets, overridden by it)")
	ram := flag.String("ram", "4G", "Max Java heap size (e.g. 2G, 4G, 8G)")
	minRam := flag.String("min-ram", "512M", "Min Java heap size (e.g. 512M, 1G)")
	mountDir := flag.String("mount", "", "FUSE mount point (default: deployments/<server-name>)")
	jarFile := flag.String("jar", "", "Server JAR file (auto-detected if not set)")
	extraJavaOpts := flag.String("java-opts", "", "Additional JVM flags")
	missingEnvs := flag.String("missing-envs", "warning", "How to handle unresolved placeholders: warning or error")
	showVersion := flag.Bool("version", false, "Show version and exit")
	dryRun := flag.Bool("dry-run", false, "Validate secrets and exit without starting")
	debug := flag.Bool("debug", false, "Enable FUSE debug logging")
	verboseFlag := flag.Bool("verbose", false, "Log key FUSE operations (Lookup ENOENT, Create, Flush, Rename)")
	restart := flag.Bool("restart", false, "Automatically restart the server on crash (not on clean exit)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `mc-fuse — Minecraft server launcher with FUSE secret injection

Usage:
  mc-fuse --secrets <file> [options] <server-directory>

Examples:
  mc-fuse --secrets lobby/secrets.enc.yaml --ram 4G servers/lobby
  mc-fuse --values values.enc.yaml --secrets lobby/secrets.enc.yaml servers/lobby
  mc-fuse --secrets secrets.enc.yaml --dry-run servers/lobby
  mc-fuse --secrets secrets.enc.yaml --restart servers/lobby

Options:
`)
		flag.PrintDefaults()
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("mc-fuse %s\n", version)
		os.Exit(0)
	}

	if *secretsFile == "" {
		log.Fatal("ERROR: --secrets is required\n\nUsage: mc-fuse --secrets <file> [options] <server-directory>")
	}

	if *missingEnvs != "warning" && *missingEnvs != "error" {
		log.Fatal("ERROR: --missing-envs must be one of: warning, error")
	}

	if flag.NArg() < 1 {
		log.Fatal("ERROR: provide server directory as argument\n\nUsage: mc-fuse --secrets <file> [options] <server-directory>")
	}

	serverDir, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		log.Fatalf("ERROR: invalid server directory: %v", err)
	}

	info, err := os.Stat(serverDir)
	if err != nil || !info.IsDir() {
		log.Fatalf("ERROR: server directory does not exist: %s", serverDir)
	}

	secretsPath, err := filepath.Abs(*secretsFile)
	if err != nil {
		log.Fatalf("ERROR: invalid secrets path: %v", err)
	}

	secrets := make(map[string]string)

	if *valuesFile != "" {
		valuesPath, err := filepath.Abs(*valuesFile)
		if err != nil {
			log.Fatalf("ERROR: invalid values path: %v", err)
		}
		log.Printf("Decrypting global values: %s", valuesPath)
		globalValues, err := loadSecrets(valuesPath)
		if err != nil {
			log.Fatalf("ERROR: %v", err)
		}
		log.Printf("Loaded %d global values", len(globalValues))
		for k, v := range globalValues {
			secrets[k] = v
		}
	}

	log.Printf("Decrypting secrets: %s", secretsPath)
	serverSecrets, err := loadSecrets(secretsPath)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}
	for k, v := range serverSecrets {
		secrets[k] = v
	}
	log.Printf("Loaded %d secrets total (%d global + %d server-specific)",
		len(secrets), len(secrets)-len(serverSecrets), len(serverSecrets))

	log.Printf("Validating placeholders in: %s", serverDir)
	errors := validateSecrets(serverDir, secrets)
	if len(errors) > 0 {
		log.Printf("%s: found %d unresolved placeholders", strings.ToUpper(*missingEnvs), len(errors))
		for _, e := range errors {
			fmt.Fprintln(os.Stderr, e)
		}
		if *missingEnvs == "error" {
			log.Fatal("ABORT: Add missing secrets to your SOPS file and try again.")
		}
		log.Println("Continuing with unresolved placeholders.")
	} else {
		log.Println("Validation OK — all placeholders have matching secrets.")
	}
	if *dryRun {
		log.Println("--dry-run: validation complete, server will not start.")
		os.Exit(0)
	}
	serverName := filepath.Base(serverDir)
	if *mountDir == "" {
		workspaceDir := filepath.Dir(filepath.Dir(serverDir))
		deploymentsDir := filepath.Join(workspaceDir, "deployments")
		*mountDir = filepath.Join(deploymentsDir, serverName)
	}
	if err := os.MkdirAll(*mountDir, 0755); err != nil {
		log.Fatalf("ERROR: cannot create mount directory: %v", err)
	}
	mountPath, err := filepath.Abs(*mountDir)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	jar := *jarFile
	if jar == "" {
		jar, err = findServerJAR(serverDir)
		if err != nil {
			log.Fatalf("ERROR: %v", err)
		}
	}
	kind := detectServerKind(serverDir, jar)

	reverseMap := buildReverseMap(secrets)

	verbose = *verboseFlag || *debug
	log.Printf("Mounting FUSE: %s → %s", serverDir, mountPath)
	root := newMCNode(serverDir, secrets, reverseMap)
	cacheTimeout := time.Hour
	server, err := fs.Mount(mountPath, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "mc-fuse:" + serverName,
			Name:       "mc-fuse",
			Debug:      *debug,
		},
		AttrTimeout:  &cacheTimeout,
		EntryTimeout: &cacheTimeout,
	})
	if err != nil {
		log.Fatalf("ERROR: FUSE mount failed: %v\nCheck if /dev/fuse exists and you have permissions.", err)
	}
	defer server.Unmount()

	log.Printf("FUSE mounted: %s", mountPath)

	javaOpts := defaultJavaOpts(kind, *minRam, *ram)
	if *extraJavaOpts != "" {
		javaOpts += " " + *extraJavaOpts
	}

	launchArgs := buildLaunchArgs(kind, jar, javaOpts)
	log.Printf("Server type: %s", kind)
	log.Printf("Starting server: java %s", strings.Join(launchArgs, " "))
	log.Printf("Working directory: %s", mountPath)

	cmd, err := launchServer(mountPath, kind, jar, javaOpts)
	if err != nil {
		log.Fatalf("ERROR: %v", err)
	}

	log.Printf("Server PID: %d", cmd.Process.Pid)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		doneCh := make(chan error, 1)
		go func() {
			doneCh <- cmd.Wait()
		}()

		var serverErr error
		cleanShutdown := false

		select {
		case sig := <-sigCh:
			log.Printf("Received signal %v, forwarding to server...", sig)
			cmd.Process.Signal(sig)
			select {
			case serverErr = <-doneCh:
			case <-time.After(30 * time.Second):
				log.Println("Timeout — force-killing server...")
				cmd.Process.Signal(syscall.SIGKILL)
				serverErr = <-doneCh
			}
			cleanShutdown = true
		case serverErr = <-doneCh:
		}

		if serverErr != nil {
			log.Printf("Server exited with error: %v", serverErr)
		} else {
			log.Println("Server exited cleanly.")
			cleanShutdown = true
		}

		if *restart && !cleanShutdown {
			log.Println("--restart: restarting server in 5 seconds... (Ctrl+C to stop)")
			select {
			case sig := <-sigCh:
				log.Printf("Received signal %v during restart wait — stopping.", sig)
				break
			case <-time.After(5 * time.Second):
			}
			if cleanShutdown {
				break
			}
			launchArgs = buildLaunchArgs(kind, jar, javaOpts)
			log.Printf("Starting server: java %s", strings.Join(launchArgs, " "))
			cmd, err = launchServer(mountPath, kind, jar, javaOpts)
			if err != nil {
				log.Fatalf("ERROR on restart: %v", err)
			}
			log.Printf("Server PID: %d", cmd.Process.Pid)
			continue
		}
		break
	}

	log.Println("Unmounting FUSE...")
	if err := server.Unmount(); err != nil {
		log.Printf("Warning: unmount failed: %v", err)
	}
	log.Println("Done.")
}
