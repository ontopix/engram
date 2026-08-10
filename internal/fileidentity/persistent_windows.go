//go:build windows

package fileidentity

import (
	"encoding/binary"
	"os"
	"syscall"
)

func persistentID(file *os.File, _ os.FileInfo) ([]byte, bool) {
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &information); err != nil {
		return nil, false
	}
	identity := make([]byte, len("windows-file-id-v1\x00")+12)
	copy(identity, "windows-file-id-v1\x00")
	offset := len("windows-file-id-v1\x00")
	binary.BigEndian.PutUint32(identity[offset:], information.VolumeSerialNumber)
	binary.BigEndian.PutUint32(identity[offset+4:], information.FileIndexHigh)
	binary.BigEndian.PutUint32(identity[offset+8:], information.FileIndexLow)
	return identity, true
}
