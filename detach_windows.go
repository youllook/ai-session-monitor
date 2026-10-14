package main

import (
	"os/exec"
	"syscall"
)

// 背景開面板：不跟著 hook 一起被收掉，也不跳黑色主控台視窗
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup, HideWindow: true}
}
