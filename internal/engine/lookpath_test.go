package engine

import "os/exec"

func execLookPath(bin string) (string, error) { return exec.LookPath(bin) }
