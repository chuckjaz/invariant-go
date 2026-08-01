package buildcache

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Cmd represents a command in the GOCACHEPROG protocol.
type Cmd string

const (
	CmdPut   = Cmd("put")
	CmdGet   = Cmd("get")
	CmdClose = Cmd("close")
)

// Request is the JSON message sent from the go command to GOCACHEPROG.
type Request struct {
	ID       int64  `json:"ID"`
	Command  Cmd    `json:"Command"`
	ActionID []byte `json:"ActionID,omitempty"`
	OutputID []byte `json:"OutputID,omitempty"`
	BodySize int64  `json:"BodySize,omitempty"`
}

// Response is the JSON response from GOCACHEPROG to the go command.
type Response struct {
	ID            int64      `json:"ID"`
	Err           string     `json:"Err,omitempty"`
	KnownCommands []Cmd      `json:"KnownCommands,omitempty"`
	Miss          bool       `json:"Miss,omitempty"`
	OutputID      []byte     `json:"OutputID,omitempty"`
	Size          int64      `json:"Size,omitempty"`
	Time          *time.Time `json:"Time,omitempty"`
	DiskPath      string     `json:"DiskPath,omitempty"`
}

// ActionEntry represents the stored metadata and content link for an ActionID in the KV store.
type ActionEntry struct {
	OutputID    []byte              `json:"output_id"`
	ContentLink content.ContentLink `json:"content_link"`
	Size        int64               `json:"size"`
	Time        time.Time           `json:"time"`
}

// CacheConfig holds configuration options for the build cache handler.
type CacheConfig struct {
	CacheDir      string
	KVStore       kv.KeyValueStore
	Storage       storage.Storage
	Slots         slots.Slots
	WriterOptions content.WriterOptions
}

// Handler processes GOCACHEPROG protocol requests.
type Handler struct {
	cfg CacheConfig
	mu  sync.Mutex // Serializes response writes
}

// NewHandler creates a new Handler with the given configuration.
func NewHandler(cfg CacheConfig) (*Handler, error) {
	if cfg.CacheDir == "" {
		cfg.CacheDir = ".invariant/build-cache"
	}
	absCacheDir, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for cache dir %s: %w", cfg.CacheDir, err)
	}
	cfg.CacheDir = absCacheDir

	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory %s: %w", cfg.CacheDir, err)
	}

	return &Handler{
		cfg: cfg,
	}, nil
}

// Start begins processing the GOCACHEPROG protocol from r and writing responses to w.
func (h *Handler) Start(r io.Reader, w io.Writer) error {
	// Step 1: Send initial handshake response with ID == 0 and KnownCommands
	initResp := Response{
		ID:            0,
		KnownCommands: []Cmd{CmdGet, CmdPut, CmdClose},
	}
	if err := h.sendResponse(w, initResp); err != nil {
		return fmt.Errorf("failed to send handshake response: %w", err)
	}

	reader := bufio.NewReader(r)
	ctx := context.Background()

	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil && err != io.EOF {
			return err
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if err == io.EOF {
				return nil
			}
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = h.sendResponse(w, Response{Err: fmt.Sprintf("invalid request JSON: %v", err)})
			continue
		}

		switch req.Command {
		case CmdGet:
			resp := h.handleGet(ctx, req)
			if err := h.sendResponse(w, resp); err != nil {
				return err
			}
		case CmdPut:
			var bodyData []byte
			if req.BodySize > 0 {
				bodyLine, err := reader.ReadBytes('\n')
				if err != nil {
					_ = h.sendResponse(w, Response{ID: req.ID, Err: fmt.Sprintf("failed to read body line: %v", err)})
					continue
				}
				bodyLine = bytes.TrimSpace(bodyLine)
				if err := json.Unmarshal(bodyLine, &bodyData); err != nil {
					_ = h.sendResponse(w, Response{ID: req.ID, Err: fmt.Sprintf("invalid body JSON string: %v", err)})
					continue
				}
			}
			resp := h.handlePut(ctx, req, bodyData)
			if err := h.sendResponse(w, resp); err != nil {
				return err
			}
		case CmdClose:
			_ = h.sendResponse(w, Response{ID: req.ID})
			return nil
		default:
			_ = h.sendResponse(w, Response{ID: req.ID, Err: fmt.Sprintf("unsupported command: %s", req.Command)})
		}
	}
}

func (h *Handler) handleGet(ctx context.Context, req Request) Response {
	if len(req.ActionID) == 0 {
		return Response{ID: req.ID, Err: "missing ActionID"}
	}

	key := fmt.Sprintf("go-build-cache:%x", req.ActionID)
	val, _, err := h.cfg.KVStore.Get(ctx, nil, key)
	if err != nil || len(val) == 0 {
		return Response{ID: req.ID, Miss: true}
	}

	var entry ActionEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		return Response{ID: req.ID, Miss: true}
	}

	fileName := hex.EncodeToString(req.ActionID)
	diskPath := filepath.Join(h.cfg.CacheDir, fileName)

	// If the file does not exist locally, download it from Invariant storage using its ContentLink
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		rc, err := content.Read(entry.ContentLink, h.cfg.Storage, h.cfg.Slots)
		if err != nil {
			return Response{ID: req.ID, Miss: true}
		}

		tmpPath := diskPath + ".tmp"
		f, err := os.Create(tmpPath)
		if err != nil {
			rc.Close()
			return Response{ID: req.ID, Err: fmt.Sprintf("failed to create local file: %v", err)}
		}

		_, copyErr := io.Copy(f, rc)
		rc.Close()
		f.Close()
		if copyErr != nil {
			os.Remove(tmpPath)
			return Response{ID: req.ID, Err: fmt.Sprintf("failed to write local file: %v", copyErr)}
		}

		if err := os.Rename(tmpPath, diskPath); err != nil {
			os.Remove(tmpPath)
			return Response{ID: req.ID, Err: fmt.Sprintf("failed to rename local file: %v", err)}
		}
	}

	return Response{
		ID:       req.ID,
		OutputID: entry.OutputID,
		DiskPath: diskPath,
		Size:     entry.Size,
		Time:     &entry.Time,
	}
}

func (h *Handler) handlePut(ctx context.Context, req Request, bodyData []byte) Response {
	if len(req.ActionID) == 0 {
		return Response{ID: req.ID, Err: "missing ActionID"}
	}

	fileName := hex.EncodeToString(req.ActionID)
	diskPath := filepath.Join(h.cfg.CacheDir, fileName)

	tmpPath := diskPath + ".tmp"
	if err := os.WriteFile(tmpPath, bodyData, 0644); err != nil {
		return Response{ID: req.ID, Err: fmt.Sprintf("failed to write local file: %v", err)}
	}
	if err := os.Rename(tmpPath, diskPath); err != nil {
		os.Remove(tmpPath)
		return Response{ID: req.ID, Err: fmt.Sprintf("failed to rename local file: %v", err)}
	}

	// Store body in Invariant content storage
	link, err := content.Write(bytes.NewReader(bodyData), h.cfg.Storage, h.cfg.WriterOptions)
	if err != nil {
		return Response{ID: req.ID, Err: fmt.Sprintf("failed to write content to storage: %v", err)}
	}

	now := time.Now().UTC()
	entry := ActionEntry{
		OutputID:    req.OutputID,
		ContentLink: link,
		Size:        int64(len(bodyData)),
		Time:        now,
	}

	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return Response{ID: req.ID, Err: fmt.Sprintf("failed to marshal action entry: %v", err)}
	}

	key := fmt.Sprintf("go-build-cache:%x", req.ActionID)
	_, err = h.cfg.KVStore.Put(ctx, nil, key, entryBytes)
	if err != nil {
		return Response{ID: req.ID, Err: fmt.Sprintf("failed to store KV entry: %v", err)}
	}

	return Response{
		ID:       req.ID,
		DiskPath: diskPath,
	}
}

func (h *Handler) sendResponse(w io.Writer, resp Response) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return err
	}

	if flusher, ok := w.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	return nil
}
