//go:build !linux && !darwin

package main

import (
	"os"
)

func getEntryTimes(info os.FileInfo) (*uint64, *uint64) {
	mtime := uint64(info.ModTime().Unix())
	return &mtime, &mtime
}
