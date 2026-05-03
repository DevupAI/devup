//go:build !linux

package main

import "fmt"

func privateMountNamespacesAvailable() bool {
	return false
}

func maybeRunMountNamespaceExec(args []string) (bool, error) {
	if len(args) > 0 && args[0] == "ns-exec" {
		return true, fmt.Errorf("mount namespace exec is only supported on linux")
	}
	return false, nil
}
