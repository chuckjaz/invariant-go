//go:build linux

package main

import (
	"os"
	"syscall"
)

func getEntryTimes(info os.FileInfo) (*uint64, *uint64) {
	mtime := uint64(info.ModTime().Unix())
	ctime := mtime
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		ctime = uint64(stat.Ctim.Sec)
	}
	return &ctime, &mtime
}
