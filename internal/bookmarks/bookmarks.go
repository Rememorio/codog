// Package bookmarks stores named pointers into Codog workspaces and sessions.
package bookmarks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Bookmark is a named pointer to a workspace, session, and optional message.
type Bookmark struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Workspace    string    `json:"workspace,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	MessageIndex *int      `json:"message_index,omitempty"`
	PRRepo       string    `json:"pr_repo,omitempty"`
	PRNumber     int       `json:"pr_number,omitempty"`
	PRURL        string    `json:"pr_url,omitempty"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Store persists bookmarks under the Codog config home.
type Store struct {
	ConfigHome string
	Now        func() time.Time
	NewID      func() (string, error)
}

// ListOptions controls workspace filtering for list and clear operations.
type ListOptions struct {
	Workspace string
	All       bool
}

// NewStore creates a bookmark store rooted at configHome.
func NewStore(configHome string) Store {
	return Store{ConfigHome: configHome}
}

// List returns bookmarks, newest first, optionally scoped to a workspace.
func (s Store) List(options ListOptions) ([]Bookmark, error) {
	bookmarks, err := s.read()
	if err != nil {
		return nil, err
	}
	filtered := make([]Bookmark, 0, len(bookmarks))
	workspace := strings.TrimSpace(options.Workspace)
	for _, bookmark := range bookmarks {
		if !options.All && workspace != "" && bookmark.Workspace != "" && bookmark.Workspace != workspace {
			continue
		}
		filtered = append(filtered, bookmark)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	return filtered, nil
}

// Add stores a bookmark and fills in its ID and timestamps when needed.
func (s Store) Add(bookmark Bookmark) (Bookmark, error) {
	name := strings.TrimSpace(bookmark.Name)
	if name == "" {
		return Bookmark{}, errors.New("bookmark name is required")
	}
	bookmarks, err := s.read()
	if err != nil {
		return Bookmark{}, err
	}
	id := strings.TrimSpace(bookmark.ID)
	if id == "" {
		id, err = s.newID()
		if err != nil {
			return Bookmark{}, err
		}
	}
	now := s.now()
	bookmark.ID = id
	bookmark.Name = name
	bookmark.Workspace = strings.TrimSpace(bookmark.Workspace)
	bookmark.SessionID = strings.TrimSpace(bookmark.SessionID)
	bookmark.PRRepo = strings.TrimSpace(bookmark.PRRepo)
	bookmark.PRURL = strings.TrimSpace(bookmark.PRURL)
	bookmark.Note = strings.TrimSpace(bookmark.Note)
	bookmark.CreatedAt = now
	bookmark.UpdatedAt = now
	bookmarks = append(bookmarks, bookmark)
	if err := s.write(bookmarks); err != nil {
		return Bookmark{}, err
	}
	return bookmark, nil
}

// Get finds the first bookmark matching ref as either ID or name.
func (s Store) Get(ref string) (Bookmark, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Bookmark{}, errors.New("bookmark id or name is required")
	}
	bookmarks, err := s.read()
	if err != nil {
		return Bookmark{}, err
	}
	for _, bookmark := range bookmarks {
		if bookmark.ID == ref || bookmark.Name == ref {
			return bookmark, nil
		}
	}
	return Bookmark{}, fmt.Errorf("bookmark not found: %s", ref)
}

// Delete removes the first bookmark matching ref as either ID or name.
func (s Store) Delete(ref string) (Bookmark, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Bookmark{}, errors.New("bookmark id or name is required")
	}
	bookmarks, err := s.read()
	if err != nil {
		return Bookmark{}, err
	}
	kept := make([]Bookmark, 0, len(bookmarks))
	var deleted Bookmark
	for _, bookmark := range bookmarks {
		if deleted.ID == "" && (bookmark.ID == ref || bookmark.Name == ref) {
			deleted = bookmark
			continue
		}
		kept = append(kept, bookmark)
	}
	if deleted.ID == "" {
		return Bookmark{}, fmt.Errorf("bookmark not found: %s", ref)
	}
	if err := s.write(kept); err != nil {
		return Bookmark{}, err
	}
	return deleted, nil
}

// Clear removes matching bookmarks and returns the number removed.
func (s Store) Clear(options ListOptions) (int, error) {
	bookmarks, err := s.read()
	if err != nil {
		return 0, err
	}
	workspace := strings.TrimSpace(options.Workspace)
	kept := make([]Bookmark, 0, len(bookmarks))
	removed := 0
	for _, bookmark := range bookmarks {
		remove := options.All || workspace == "" || bookmark.Workspace == "" || bookmark.Workspace == workspace
		if remove {
			removed++
			continue
		}
		kept = append(kept, bookmark)
	}
	if err := s.write(kept); err != nil {
		return 0, err
	}
	return removed, nil
}

// Path returns the bookmark JSON file path.
func (s Store) Path() (string, error) {
	if strings.TrimSpace(s.ConfigHome) == "" {
		return "", errors.New("config home is unavailable")
	}
	return filepath.Join(s.ConfigHome, "bookmarks.json"), nil
}

func (s Store) read() ([]Bookmark, error) {
	path, err := s.Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var bookmarks []Bookmark
	if err := json.Unmarshal(data, &bookmarks); err != nil {
		return nil, err
	}
	return bookmarks, nil
}

func (s Store) write(bookmarks []Bookmark) error {
	path, err := s.Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bookmarks, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) newID() (string, error) {
	if s.NewID != nil {
		return s.NewID()
	}
	var data [4]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "bm-" + hex.EncodeToString(data[:]), nil
}
