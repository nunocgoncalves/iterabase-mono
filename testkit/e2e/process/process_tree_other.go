//go:build !darwin && !linux

package process

import "os/exec"

func configureProcessTree(_ *exec.Cmd) {}
