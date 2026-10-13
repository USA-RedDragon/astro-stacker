//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

func ignoreBackgroundRead() { signal.Ignore(syscall.SIGTTIN) }
