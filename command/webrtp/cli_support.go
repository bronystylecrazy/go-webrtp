package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bronystylecrazy/go-webrtp/streamcore"
)

type RecordingFileResponse struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	SizeBytes    int64     `json:"sizeBytes"`
	LastModified time.Time `json:"lastModified"`
}

func loadConfig(path string) (*streamcore.Config, error) {
	cfg, err := streamcore.LoadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	for _, upstream := range cfg.Upstreams {
		if err := streamcore.ValidateUpstream(upstream); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func RecordingsList(root string) ([]*RecordingFileResponse, error) {
	entries := make([]*RecordingFileResponse, 0)
	if strings.TrimSpace(root) == "" {
		root = "recordings"
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, fmt.Errorf("stat recordings root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("recordings root is not a directory: %s", root)
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || info == nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, &RecordingFileResponse{
			Name:         filepath.Base(path),
			Path:         filepath.ToSlash(rel),
			SizeBytes:    info.Size(),
			LastModified: info.ModTime(),
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk recordings root: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].LastModified.Equal(entries[j].LastModified) {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].LastModified.After(entries[j].LastModified)
	})
	return entries, nil
}
