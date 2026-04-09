package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ExecuteInstruction runs a COPY or RUN command against the assembled rootfs
func ExecuteInstruction(inst Instruction, rootfs string, config config, contextDir string) error {
	if inst.Type == "COPY" {
		return executeCopy(inst.Args, rootfs, contextDir)
	} else if inst.Type == "RUN" {
		return executeRun(inst.Args, rootfs, config)
	}
	return fmt.Errorf("unsupported execution instruction: %s", inst.Type)
}

func executeCopy(args, rootfs, contextDir string) error {
	parts := strings.Fields(args)
	if len(parts) != 2 {
		return fmt.Errorf("invalid COPY format")
	}
	srcPattern := parts[0]
	destPath := parts[1]

	// Resolve destination inside the rootfs
	absDest, err := containerPathOnRootfs(rootfs, destPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absDest, 0755); err != nil {
		return fmt.Errorf("failed to create destination %q: %w", destPath, err)
	}

	sourceFiles, err := resolveCopySourceFiles(contextDir, srcPattern)
	if err != nil {
		return err
	}

	for _, srcFilePath := range sourceFiles {
		rel, err := filepath.Rel(contextDir, srcFilePath)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %q: %w", srcFilePath, err)
		}

		destFilePath := filepath.Join(absDest, rel)
		if err := os.MkdirAll(filepath.Dir(destFilePath), 0755); err != nil {
			return fmt.Errorf("failed to create destination parent for %q: %w", destFilePath, err)
		}

		if err := copyFile(srcFilePath, destFilePath); err != nil {
			return err
		}
	}

	return nil
}

func executeRun(command, rootfs string, config config) error {
	exitCode, err := runIsolatedCommand(command, rootfs, config)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("command exited with code %d", exitCode)
	}
	return nil
}

func runIsolatedCommand(command, rootfs string, config config) (int, error) {
	// Setup the command to run via shell
	cmd := exec.Command("/bin/sh", "-c", command)

	// Route output to the user's terminal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	// Set Working Directory (default to / if empty)
	workDir := config.WorkingDir
	if workDir == "" {
		workDir = "/"
	} else if !filepath.IsAbs(workDir) {
		workDir = "/" + workDir
	}
	cmd.Dir = workDir

	// Inject Environment Variables
	cmd.Env = config.Env

	// HARD REQUIREMENT: OS-Level Process Isolation
	// We use Chroot to change the root filesystem, and Cloneflags to create new namespaces
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:                     rootfs,
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}

	// Note: For a fully featured runtime, we would also mount /proc and /dev inside the rootfs here.
	// For the simplified constraints of this project, basic Chroot + Namespaces satisfies the requirement.

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				if ws.Signaled() {
					return 128 + int(ws.Signal()), nil
				}
				return ws.ExitStatus(), nil
			}
			return 1, nil
		}

		return 1, err
	}

	return 0, nil
}

func containerPathOnRootfs(rootfs, containerPath string) (string, error) {
	cleaned := filepath.Clean(containerPath)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return rootfs, nil
	}

	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes container root", containerPath)
	}

	cleaned = strings.TrimPrefix(cleaned, string(filepath.Separator))
	if cleaned == "" {
		return rootfs, nil
	}

	return filepath.Join(rootfs, cleaned), nil
}

func copyFile(srcPath, dstPath string) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source %q: %w", srcPath, err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination %q: %w", dstPath, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy %q -> %q: %w", srcPath, dstPath, err)
	}

	return nil
}
