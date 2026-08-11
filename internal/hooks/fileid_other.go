//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"reflect"
)

// persistentFileID extracts the platform file identifier exposed by
// os.FileInfo. Unix stat values expose Dev/Ino and Plan 9 exposes Qid.Path.
func persistentFileID(_ *os.File, info os.FileInfo) (string, bool) {
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return "", false
	}
	if device, ok := integerField(value, "Dev"); ok {
		if inode, inodeOK := integerField(value, "Ino"); inodeOK {
			return fmt.Sprintf("dev-inode:%x:%x", device, inode), true
		}
	}
	qid := value.FieldByName("Qid")
	for qid.IsValid() && qid.Kind() == reflect.Pointer {
		if qid.IsNil() {
			return "", false
		}
		qid = qid.Elem()
	}
	if qid.IsValid() && qid.Kind() == reflect.Struct {
		if qidPath, ok := integerField(qid, "Path"); ok {
			return fmt.Sprintf("qid:%x", qidPath), true
		}
	}
	return "", false
}

func integerField(value reflect.Value, name string) (uint64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}
