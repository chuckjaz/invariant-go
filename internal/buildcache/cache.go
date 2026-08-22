package buildcache

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

const DefaultInMemoryCapacity = 100000

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
	CacheDir         string
	KVStore          kv.KeyValueStore
	Storage          storage.Storage
	Slots            slots.Slots
	WriterOptions    content.WriterOptions
	InMemoryCapacity int
	WriteTag         string
}

type lruItem struct {
	key   string
	entry ActionEntry
}

// Handler processes GOCACHEPROG protocol requests.
type Handler struct {
	cfg      CacheConfig
	mu       sync.Mutex // Serializes response writes
	lruMu    sync.Mutex
	lruList  *list.List
	lruMap   map[string]*list.Element
	capacity int
	wg       sync.WaitGroup
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

	capacity := cfg.InMemoryCapacity
	if capacity <= 0 {
		capacity = DefaultInMemoryCapacity
	}

	writeTag := cfg.WriteTag
	if writeTag == "" {
		writeTag = "generated"
	}
	if strings.EqualFold(writeTag, "any") {
		writeTag = ""
	}
	if cfg.Storage != nil {
		if tagged, ok := cfg.Storage.(storage.TaggedStorage); ok {
			cfg.Storage = tagged.WithWriteTag(writeTag)
		}
	}

	return &Handler{
		cfg:      cfg,
		lruList:  list.New(),
		lruMap:   make(map[string]*list.Element),
		capacity: capacity,
	}, nil
}

// Wait waits for all async background KV puts to complete.
func (h *Handler) Wait() {
	h.wg.Wait()
}

func (h *Handler) getMemory(key string) (ActionEntry, bool) {
	h.lruMu.Lock()
	defer h.lruMu.Unlock()

	if el, ok := h.lruMap[key]; ok {
		h.lruList.MoveToFront(el)
		return el.Value.(*lruItem).entry, true
	}
	return ActionEntry{}, false
}

func (h *Handler) putMemory(key string, entry ActionEntry) {
	h.lruMu.Lock()
	defer h.lruMu.Unlock()

	if el, ok := h.lruMap[key]; ok {
		el.Value.(*lruItem).entry = entry
		h.lruList.MoveToFront(el)
		return
	}

	item := &lruItem{
		key:   key,
		entry: entry,
	}
	el := h.lruList.PushFront(item)
	h.lruMap[key] = el

	if h.lruList.Len() > h.capacity {
		back := h.lruList.Back()
		if back != nil {
			h.lruList.Remove(back)
			delete(h.lruMap, back.Value.(*lruItem).key)
		}
	}
}

func (h *Handler) updateMemoryContentLink(key string, link content.ContentLink) {
	h.lruMu.Lock()
	defer h.lruMu.Unlock()

	if el, ok := h.lruMap[key]; ok {
		el.Value.(*lruItem).entry.ContentLink = link
	}
}

// Start begins processing the GOCACHEPROG protocol from r and writing responses to w.
func (h *Handler) Start(r io.Reader, w io.Writer) error {
	defer h.wg.Wait()

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
			h.wg.Wait()
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

	fileName := hex.EncodeToString(req.ActionID)
	diskPath := filepath.Join(h.cfg.CacheDir, fileName)

	// Step 1: Check in-memory map
	entry, inMem := h.getMemory(fileName)
	if inMem {
		// Requirement 2: If a get is in the in memory map and the file is on disk already, return early.
		if _, err := os.Stat(diskPath); err == nil {
			return Response{
				ID:       req.ID,
				OutputID: entry.OutputID,
				DiskPath: diskPath,
				Size:     entry.Size,
				Time:     &entry.Time,
			}
		}

		// File is in memory, but not on disk. Download using ContentLink if available.
		if entry.ContentLink.Address != "" || entry.ContentLink.Slot {
			if err := h.downloadToDisk(entry.ContentLink, diskPath); err == nil {
				return Response{
					ID:       req.ID,
					OutputID: entry.OutputID,
					DiskPath: diskPath,
					Size:     entry.Size,
					Time:     &entry.Time,
				}
			}
		}
	}

	// Step 2: Check KV store backup if not in memory (or file missing & link unavailable)
	if h.cfg.KVStore == nil {
		return Response{ID: req.ID, Miss: true}
	}

	kvKey := fmt.Sprintf("go-build-cache:%x", req.ActionID)
	val, _, err := h.cfg.KVStore.Get(ctx, nil, kvKey)
	if err != nil || len(val) == 0 {
		return Response{ID: req.ID, Miss: true}
	}

	var kvEntry ActionEntry
	if err := json.Unmarshal(val, &kvEntry); err != nil {
		return Response{ID: req.ID, Miss: true}
	}

	// Bring into memory
	h.putMemory(fileName, kvEntry)

	// Check local disk file
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		if err := h.downloadToDisk(kvEntry.ContentLink, diskPath); err != nil {
			return Response{ID: req.ID, Err: fmt.Sprintf("failed to download file: %v", err)}
		}
	}

	return Response{
		ID:       req.ID,
		OutputID: kvEntry.OutputID,
		DiskPath: diskPath,
		Size:     kvEntry.Size,
		Time:     &kvEntry.Time,
	}
}

func (h *Handler) downloadToDisk(link content.ContentLink, diskPath string) error {
	rc, err := content.Read(link, h.cfg.Storage, h.cfg.Slots)
	if err != nil {
		return err
	}
	defer rc.Close()

	tmpPath := diskPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(f, rc)
	f.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return copyErr
	}

	if err := os.Rename(tmpPath, diskPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return nil
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

	now := time.Now().UTC()
	entry := ActionEntry{
		OutputID: req.OutputID,
		Size:     int64(len(bodyData)),
		Time:     now,
	}

	// Add to in-memory map
	h.putMemory(fileName, entry)

	// Increment WaitGroup and schedule KV/storage put in parallel
	h.wg.Add(1)
	go func(actionID []byte, outputID []byte, body []byte, t time.Time, keyName string) {
		defer h.wg.Done()

		var link content.ContentLink
		if h.cfg.Storage != nil {
			l, err := content.Write(bytes.NewReader(body), h.cfg.Storage, h.cfg.WriterOptions)
			if err != nil {
				return
			}
			link = l
			h.updateMemoryContentLink(keyName, link)
		}

		if h.cfg.KVStore != nil {
			bgEntry := ActionEntry{
				OutputID:    outputID,
				ContentLink: link,
				Size:        int64(len(body)),
				Time:        t,
			}

			entryBytes, err := json.Marshal(bgEntry)
			if err == nil {
				kvKey := fmt.Sprintf("go-build-cache:%x", actionID)
				_, _ = h.cfg.KVStore.Put(context.Background(), nil, kvKey, entryBytes)
			}
		}
	}(req.ActionID, req.OutputID, bodyData, now, fileName)

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
