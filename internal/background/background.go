package background

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Task struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	PID       int       `json:"pid"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	LogPath   string    `json:"log_path"`
}

type Store struct {
	Dir string
}

func NewStore(configHome string) Store {
	return Store{Dir: filepath.Join(configHome, "background")}
}

func (s Store) Run(command string, cwd string) (Task, error) {
	if strings.TrimSpace(command) == "" {
		return Task{}, errors.New("background command is required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Task{}, err
	}
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	logPath := filepath.Join(s.Dir, id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Task{}, err
	}
	defer logFile.Close()

	cmd := exec.Command("sh", "-lc", command)
	cmd.Dir = cwd
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return Task{}, err
	}
	task := Task{
		ID:        id,
		Command:   command,
		PID:       cmd.Process.Pid,
		Status:    "running",
		StartedAt: time.Now().UTC(),
		LogPath:   logPath,
	}
	if err := s.save(task); err != nil {
		return Task{}, err
	}
	go func() {
		err := cmd.Wait()
		task.Status = "completed"
		if err != nil {
			task.Status = "failed"
		}
		_ = s.save(task)
	}()
	return task, nil
}

func (s Store) List() ([]Task, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})
	return tasks, nil
}

func (s Store) Get(id string) (Task, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) save(task Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, task.ID+".json"), data, 0o644)
}
