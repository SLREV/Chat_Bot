package store

import "os"

// openAppend opens a file for appending (test helper kept in one place so the
// production file does not need an exported escape hatch).
func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
