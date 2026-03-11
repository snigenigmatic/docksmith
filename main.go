package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func main() {
	// Internal subcommand: runs inside new namespaces to set up container
	if len(os.Args) >= 2 && os.Args[1] == "__container_init" {
		if err := containerInit(); err != nil {
			fmt.Fprintln(os.Stderr, "container init:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := InitDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "build":
		err = cmdBuild(os.Args[2:])
	case "images":
		err = cmdImages()
	case "rmi":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: docksmith rmi <name:tag>")
			os.Exit(1)
		}
		err = cmdRmi(os.Args[2])
	case "run":
		err = cmdRun(os.Args[2:])
	case "import-base":
		err = cmdImportBase(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// containerInit runs as the first process inside newly created namespaces.
// It performs chroot, mounts /proc, sets up the working directory, then
// execs the real user command — replacing this process image entirely.
func containerInit() error {
	rootfs := os.Getenv("_DS_ROOTFS")
	cmdJSON := os.Getenv("_DS_CMD")
	workdir := os.Getenv("_DS_WORKDIR")

	if rootfs == "" {
		return fmt.Errorf("_DS_ROOTFS not set")
	}
	if cmdJSON == "" {
		return fmt.Errorf("_DS_CMD not set")
	}

	// Chroot into the assembled container filesystem.
	// We have CAP_SYS_CHROOT because we are mapped as UID 0 inside a user namespace.
	if err := syscall.Chroot(rootfs); err != nil {
		return fmt.Errorf("chroot(%q): %w", rootfs, err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Mount /proc inside the new PID+mount namespace (best-effort).
	_ = os.MkdirAll("/proc", 0755)
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")

	// Parse the command to execute.
	var cmd []string
	if err := json.Unmarshal([]byte(cmdJSON), &cmd); err != nil {
		return fmt.Errorf("parse _DS_CMD: %w", err)
	}
	if len(cmd) == 0 {
		return fmt.Errorf("empty command")
	}

	// Strip all _DS_* control variables from the environment before handing off.
	var cleanEnv []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "_DS_") {
			cleanEnv = append(cleanEnv, e)
		}
	}

	// Apply working directory (failures are non-fatal; the process starts at /).
	if workdir != "" {
		_ = os.Chdir(workdir)
	}

	// Resolve executable path inside the chroot.
	execPath, err := lookupExec(cmd[0])
	if err != nil {
		return fmt.Errorf("executable not found: %s", cmd[0])
	}

	// Replace this process with the user command.
	return syscall.Exec(execPath, cmd, cleanEnv)
}

// lookupExec finds an executable by name using PATH (searched inside whatever
// root the calling process sees, so it works correctly after chroot).
func lookupExec(name string) (string, error) {
	if strings.Contains(name, "/") {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
		return "", fmt.Errorf("not found")
	}
	pathVar := os.Getenv("PATH")
	if pathVar == "" {
		pathVar = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	for _, dir := range strings.Split(pathVar, ":") {
		if dir == "" {
			continue
		}
		full := dir + "/" + name
		if _, err := os.Stat(full); err == nil {
			return full, nil
		}
	}
	return "", fmt.Errorf("not found in PATH")
}

func printUsage() {
	fmt.Fprint(os.Stderr, `Docksmith — a simplified container build and runtime system

Usage:
  docksmith build -t <name:tag> [--no-cache] <context>   Build image from Docksmithfile
  docksmith images                                         List local images
  docksmith rmi <name:tag>                                Remove image and its layers
  docksmith run [-e KEY=VALUE] <name:tag> [cmd...]        Run a container
  docksmith import-base <name:tag> <layer.tar>            Import a base image layer

`)
}

// mustJSON serialises v to JSON, panicking on error (only used for known-good types).
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// parseNameTag splits "name:tag" into its parts; tag defaults to "latest".
func parseNameTag(nameTag string) (name, tag string) {
	idx := strings.LastIndex(nameTag, ":")
	if idx < 0 {
		return nameTag, "latest"
	}
	return nameTag[:idx], nameTag[idx+1:]
}