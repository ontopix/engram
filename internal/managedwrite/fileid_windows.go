//go:build windows

package managedwrite

import (
	"fmt"
	"os"
	"syscall"
)

func persistentFileID(file *os.File, _ os.FileInfo) (string, bool) {
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &information); err != nil {
		return "", false
	}
	return fmt.Sprintf("volume-index:%x:%x:%x", information.VolumeSerialNumber, information.FileIndexHigh, information.FileIndexLow), true
}
