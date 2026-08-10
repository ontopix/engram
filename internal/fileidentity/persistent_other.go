//go:build !windows

package fileidentity

import (
	"encoding/binary"
	"os"
	"reflect"
	"strings"
)

func persistentID(_ *os.File, info os.FileInfo) ([]byte, bool) {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return nil, false
	}
	var device, inode uint64
	var haveDevice, haveInode bool
	typeInfo := value.Type()
	for index := 0; index < value.NumField(); index++ {
		name := strings.ToLower(typeInfo.Field(index).Name)
		field := value.Field(index)
		read := func() (uint64, bool) {
			switch field.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				return uint64(field.Int()), true
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				return field.Uint(), true
			default:
				return 0, false
			}
		}
		switch name {
		case "dev":
			device, haveDevice = read()
		case "ino":
			inode, haveInode = read()
		}
	}
	if !haveDevice || !haveInode {
		return nil, false
	}
	identity := make([]byte, len("unix-file-id-v1\x00")+16)
	copy(identity, "unix-file-id-v1\x00")
	offset := len("unix-file-id-v1\x00")
	binary.BigEndian.PutUint64(identity[offset:], device)
	binary.BigEndian.PutUint64(identity[offset+8:], inode)
	return identity, true
}
