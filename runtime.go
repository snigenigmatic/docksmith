package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// RunContainer assembles the given rootfs and executes cmd inside a new set of
// Linux namespaces (PID, mount, user).  The same function is used for both
// `docksmith run` and RUN instructions during a build — one primitive, two uses.
//
// The process isolation works as follows:
//  1. The parent spawns itself (__container_init subcommand) inside a new
//     PID + mount + user namespace, passing rootfs/cmd/workdir via env.
//  2. __container_init (running as mapped root inside the user namespace)
//     calls chroot(rootfs), mounts /proc, then execs the real command.
//
// env should already contain the final KEY=VALUE pairs to inject; -e overrides
// are the caller's responsibility.
func RunContainer(rootfs string, cmd []string, env []string, workdir string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("resolve self path: %w", err)
	}

	// Pass control variables to the init process.
	childEnv := make([]string, len(env))
	copy(childEnv, env)
	childEnv = append(childEnv,
		"_DS_ROOTFS="+rootfs,
		"_DS_CMD="+mustJSON(cmd),
		"_DS_WORKDIR="+workdir,
	)

	c := &exec.Cmd{
		Path:   self,
		Args:   []string{self, "__container_init"},
		Env:    childEnv,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{
			// New PID namespace:   container processes can't see host PIDs.
			// New mount namespace: mounts inside the container don't affect the host.
			// New user namespace:  map current user → root so chroot/mount work
			//                      without host-level privileges.
			Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWUSER,
			UidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getuid(), Size: 1},
			},
			GidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getgid(), Size: 1},
			},
		},
	}

	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// AssembleRootfs extracts all layers of an image into a fresh temporary
// directory in order (later layers overwrite earlier ones) and returns the path.
// The caller is responsible for removing the directory when done.
func AssembleRootfs(m *ImageManifest) (string, error) {
	tmpDir, err := os.MkdirTemp("", "docksmith-run-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	rootfs := tmpDir + "/rootfs"
	if err := os.MkdirAll(rootfs, 0755); err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}

	for _, l := range m.Layers {
		if err := ExtractLayer(l.Digest, rootfs); err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("extract layer %s: %w", l.Digest[:16], err)
		}
	}
	return tmpDir, nil
}