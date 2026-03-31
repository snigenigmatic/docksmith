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
	if len(parts) != 2 {
		return fmt.Errorf("invalid COPY format")
	}
	srcPattern := parts[0]
	destPath := parts[1]

	matches, err := resolveCopySources(contextDir, srcPattern)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("COPY source %q matched no files in context %q", srcPattern, contextDir)
	}

	destPath = normalizeContainerPath(destPath)
	absDest := filepath.Join(rootfs, strings.TrimPrefix(destPath, "/"))
	if err := os.MkdirAll(absDest, 0755); err != nil {
		return err
	}

	for _, path := range matches {
		rel, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}

		destFilePath := filepath.Join(absDest, filepath.Base(path))
		if srcPattern == "." || strings.Contains(srcPattern, string(filepath.Separator)) || strings.Contains(srcPattern, "/") || strings.Contains(srcPattern, "*") {
			destFilePath = filepath.Join(absDest, rel)
		}

		if err := os.MkdirAll(filepath.Dir(destFilePath), 0755); err != nil {
			return err
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}

		dstFile, err := os.Create(destFilePath)
		if err != nil {
			srcFile.Close()
			return err
		}

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			dstFile.Close()
			srcFile.Close()
			return err
		}
		if err := dstFile.Close(); err != nil {
			srcFile.Close()
			return err
		}
		if err := srcFile.Close(); err != nil {
			return err
		}
	}

	return nil
}

func executeRun(command, rootfs string, config config) error {
	cmd, err := newIsolatedCommand(rootfs, config, []string{"/bin/sh", "-c", command})
	if err != nil {
		return err
	}
	return cmd.Run()
}

func newIsolatedCommand(rootfs string, config config, argv []string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command specified")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Dir = normalizeContainerPath(config.WorkingDir)
	cmd.Env = config.Env

	// We use the same Chroot + namespace isolation primitive for both build RUN and docksmith run.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     rootfs,
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}

	return cmd, nil
}

func normalizeContainerPath(path string) string {
	if path == "" {
		return "/"
	}
	if filepath.IsAbs(path) {
		return path
	}
	return "/" + path
}

func resolveContainerCommand(rootfs string, argv []string, env []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command specified")
	}

	if strings.Contains(argv[0], "/") {
		return argv, nil
	}

	searchPaths := []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}
	for _, envVar := range env {
		if strings.HasPrefix(envVar, "PATH=") {
			searchPaths = strings.Split(strings.TrimPrefix(envVar, "PATH="), ":")
			break
		}
	}

	for _, dir := range searchPaths {
		hostPath := filepath.Join(rootfs, strings.TrimPrefix(dir, "/"), argv[0])
		info, err := os.Lstat(hostPath)
		if err != nil || info.IsDir() {
			continue
		}
		resolved := make([]string, len(argv))
		copy(resolved, argv)
		resolved[0] = normalizeContainerPath(filepath.Join(dir, argv[0]))
		return resolved, nil
	}

	return nil, fmt.Errorf("command %q not found in container PATH", argv[0])
}
