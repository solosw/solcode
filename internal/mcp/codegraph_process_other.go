//go:build !windows

package mcp

import "os/exec"

func configureCodeGraphProcess(*exec.Cmd) {}
