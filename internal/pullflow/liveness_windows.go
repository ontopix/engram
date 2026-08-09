//go:build windows

package pullflow

import "errors"

func processAlive(int) (bool, error) {
	return false, errors.New("portable foreign-process liveness proof is unavailable on Windows")
}
