package main

import (
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
	if len(parts) < 2 {
		return fmt.Errorf("invalid COPY format")
	}
	srcPattern := parts[0]
	destPath := parts[1]

	// Resolve destination inside the rootfs
	absDest := filepath.Join(rootfs, destPath)
	os.MkdirAll(absDest, 0755)

	// Simplified copy logic (in a real app, handle full globbing and recursive dir copies)
	// For now, we copy files that match the pattern from the context dir
	return filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		rel, _ := filepath.Rel(contextDir, path)
		match, _ := filepath.Match(srcPattern, rel)
		if srcPattern == "." || match {
			// Copy file
			srcFile, _ := os.Open(path)
			defer srcFile.Close()

			destFilePath := filepath.Join(absDest, filepath.Base(path))
			if srcPattern == "." {
				destFilePath = filepath.Join(absDest, rel)
				os.MkdirAll(filepath.Dir(destFilePath), 0755)
			}

			dstFile, _ := os.Create(destFilePath)
			defer dstFile.Close()
			io.Copy(dstFile, srcFile)
		}
		return nil
	})
}

func executeRun(command, rootfs string, config config) error {
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
	}
	cmd.Dir = workDir

	// Inject Environment Variables
	cmd.Env = config.Env

	// HARD REQUIREMENT: OS-Level Process Isolation
	// We use Chroot to change the root filesystem, and Cloneflags to create new namespaces
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     rootfs,
		Cloneflags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS,
	}

	// Note: For a fully featured runtime, we would also mount /proc and /dev inside the rootfs here.
	// For the simplified constraints of this project, basic Chroot + Namespaces satisfies the requirement.

	return cmd.Run()
}
