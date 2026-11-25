package server

import "os/exec"

// lookPath is a seam so tests can fake engine availability.
func lookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
