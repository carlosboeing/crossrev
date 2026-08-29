//go:build linux

package ui

import "syscall"

const ioctlReadTermios = syscall.TCGETS
