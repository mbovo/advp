//go:build !linux && !darwin

package main

import (
	"errors"
	"runtime"
)

func openICMP(cfg sockConfig) (icmpConn, error) {
	return nil, errors.New("ICMP sockets are not supported on " + runtime.GOOS + " (only linux and darwin)")
}

func localErrorText(e *Event) string { return "local error" }
