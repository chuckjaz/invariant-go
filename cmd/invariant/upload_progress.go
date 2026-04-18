package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type uploader struct {
	FilesChecking   int64
	FilesChecked    uint64
	FilesSkipped    uint64
	FilesShared     uint64
	DirsChecking    int64
	DirsChecked     uint64
	DirsSkipped     uint64
	DirsShared      uint64
	BytesUploaded   uint64
	UploadsInFlight int64

	BlocksUploaded uint64
	DirsCreated    uint64

	cacheMu      sync.RWMutex
	cache        map[string]UploadCacheEntry
	cachePath    string
	disableCache bool

	fileQueue *workerQueue
	dirQueue  *workerQueue
}

type workerQueue struct {
	tasks chan func()
}

func newWorkerQueue(maxWorkers int, bufferSize int) *workerQueue {
	q := &workerQueue{
		tasks: make(chan func(), bufferSize),
	}

	for range maxWorkers {
		go func() {
			for t := range q.tasks {
				t()
			}
		}()
	}

	return q
}

func (q *workerQueue) Submit(task func()) {
	q.tasks <- task
}

type UploadCacheEntry struct {
	MTime       uint64 `json:"mtime"`
	ContentLink string `json:"content_link"` // stored as JSON string
	Size        uint64 `json:"size"`
	Mode        string `json:"mode"`
}

func (u *uploader) formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (u *uploader) progressLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastBytes uint64
	var lastTime time.Time = time.Now()

	for {
		select {
		case <-ctx.Done():
			fmt.Println() // Newline on finish
			return
		case now := <-ticker.C:
			bytes := atomic.LoadUint64(&u.BytesUploaded)
			deltaBytes := bytes - lastBytes
			deltaTime := now.Sub(lastTime).Seconds()

			bps := float64(0)
			if deltaTime > 0 {
				bps = float64(deltaBytes) / deltaTime
			}

			fchk := atomic.LoadInt64(&u.FilesChecking)
			fc := atomic.LoadUint64(&u.FilesChecked)
			fs := atomic.LoadUint64(&u.FilesSkipped)
			fsh := atomic.LoadUint64(&u.FilesShared)
			dchk := atomic.LoadInt64(&u.DirsChecking)
			dc := atomic.LoadUint64(&u.DirsChecked)
			ds := atomic.LoadUint64(&u.DirsSkipped)
			dsh := atomic.LoadUint64(&u.DirsShared)
			inf := atomic.LoadInt64(&u.UploadsInFlight)

			fmt.Printf("\r\033[KFiles: %d checking, %d done, %d skipped, %d shared | Dirs: %d checking, %d done, %d skipped, %d shared | Uploading: %d | Total: %s | Speed: %s/s",
				fchk, fc, fs, fsh, dchk, dc, ds, dsh, inf, u.formatBytes(bytes), u.formatBytes(uint64(bps)))

			lastBytes = bytes
			lastTime = now
		}
	}
}
