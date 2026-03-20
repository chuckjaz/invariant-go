package nfs

import (
	"context"
	"io"
	"os"
	"sync"
	"syscall"
)

type File struct {
	mu       sync.Mutex
	fs       *FS
	nodeID   uint64
	name     string
	flag     int
	position int64

	f      *os.File
	dirty  bool
	reader io.ReadCloser
}

func (file *File) Name() string {
	return file.name
}

func (file *File) Write(p []byte) (n int, err error) {
	file.mu.Lock()
	defer file.mu.Unlock()

	if err := file.ensureWriteable(); err != nil {
		return 0, err
	}

	n, err = file.f.WriteAt(p, file.position)
	if n > 0 {
		file.position += int64(n)
		file.dirty = true
	}
	return n, err
}

func (file *File) Read(p []byte) (n int, err error) {
	file.mu.Lock()
	defer file.mu.Unlock()

	if file.f != nil {
		n, err = file.f.ReadAt(p, file.position)
		if n > 0 {
			file.position += int64(n)
		}
		if err == io.EOF {
			return 0, io.EOF
		}
		return n, err
	}

	if file.reader == nil {
		ctx := context.Background()
		r, err := file.fs.fsrv.ReadFile(ctx, file.nodeID, file.position, 0)
		if err != nil {
			return 0, err
		}
		file.reader = r
	}

	n, err = io.ReadFull(file.reader, p)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if n > 0 {
		file.position += int64(n)
	}
	return n, err
}

func (file *File) ReadAt(p []byte, off int64) (n int, err error) {
	file.mu.Lock()
	defer file.mu.Unlock()

	if file.f != nil {
		return file.f.ReadAt(p, off)
	}

	if file.reader != nil {
		file.reader.Close()
		file.reader = nil
	}

	ctx := context.Background()
	r, err := file.fs.fsrv.ReadFile(ctx, file.nodeID, off, int64(len(p)))
	if err != nil {
		return 0, err
	}
	defer r.Close()

	n, err = io.ReadFull(r, p)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n, err
}

func (file *File) Seek(offset int64, whence int) (int64, error) {
	file.mu.Lock()
	defer file.mu.Unlock()

	var newPos int64
	var err error
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = file.position + offset
	case io.SeekEnd:
		if file.f != nil {
			stat, statErr := file.f.Stat()
			if statErr != nil {
				return 0, statErr
			}
			newPos = stat.Size() + offset
		} else {
			ctx := context.Background()
			attrs, getErr := file.fs.fsrv.GetAttributes(ctx, file.nodeID)
			if getErr != nil || attrs.Size == nil {
				return 0, syscall.EIO
			}
			newPos = int64(*attrs.Size) + offset
		}
	}

	if newPos < 0 {
		return 0, syscall.EINVAL
	}

	if file.f != nil {
		file.position = newPos
	} else {
		if file.reader != nil && file.position != newPos {
			if seeker, ok := file.reader.(io.Seeker); ok {
				_, err = seeker.Seek(newPos, io.SeekStart)
				if err == nil {
					file.position = newPos
				} else {
					file.reader.Close()
					file.reader = nil
					file.position = newPos
				}
			} else {
				file.reader.Close()
				file.reader = nil
				file.position = newPos
			}
		} else {
			file.position = newPos
		}
	}

	return newPos, nil
}

func (file *File) ensureWriteable() error {
	if file.f != nil {
		return nil
	}

	f, err := os.CreateTemp("", "invariant-nfs-*")
	if err != nil {
		return err
	}
	os.Remove(f.Name()) // Unlink to delete on close
	file.f = f

	// Populate if it already has content and wasn't truncated
	if file.flag&os.O_TRUNC == 0 {
		ctx := context.Background()
		reader, err := file.fs.fsrv.ReadFile(ctx, file.nodeID, 0, 0)
		if err == nil {
			io.Copy(f, reader)
			reader.Close()
		}
	}

	if file.reader != nil {
		file.reader.Close()
		file.reader = nil
	}

	return nil
}

func (file *File) Close() error {
	file.mu.Lock()
	defer file.mu.Unlock()

	if file.f != nil && file.dirty {
		file.f.Seek(0, io.SeekStart)
		ctx := context.Background()
		err := file.fs.fsrv.WriteFile(ctx, file.nodeID, 0, false, file.f)
		if err != nil {
			return err
		}
		file.dirty = false
	}

	if file.f != nil {
		file.f.Close()
		file.f = nil
	}
	if file.reader != nil {
		file.reader.Close()
		file.reader = nil
	}
	return nil
}

func (file *File) Lock() error {
	return nil // not properly supported yet
}

func (file *File) Unlock() error {
	return nil // not properly supported yet
}

func (file *File) Truncate(size int64) error {
	file.mu.Lock()
	defer file.mu.Unlock()

	if err := file.ensureWriteable(); err != nil {
		return err
	}
	err := file.f.Truncate(size)
	if err == nil {
		file.dirty = true
	}
	return err
}
