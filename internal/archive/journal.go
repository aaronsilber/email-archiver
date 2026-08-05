package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Journal records what a run has moved so far.
//
// It is deliberately advisory. Correctness comes from the drain loop — the
// query shrinks as messages move, so re-running the same command resumes
// exactly where an interrupted run stopped. The journal exists so the tool can
// tell the user "a previous run moved N messages" instead of making them guess.
type Journal struct {
	Version   int            `json:"version"`
	Key       string         `json:"key"`
	Args      []string       `json:"args"`
	StartedAt time.Time      `json:"startedAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Moved     map[string]int `json:"movedByMailbox"`
	Batches   int            `json:"batches"`

	path    string
	saveErr error
}

const journalVersion = 1

// StateDir returns the directory holding run journals, honoring XDG_STATE_HOME.
func StateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "email-archiver"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "email-archiver"), nil
}

// JournalKey derives a stable identifier from the arguments that define a run,
// so that re-running the same command finds the same journal.
func JournalKey(account string, o Options, sources []string) string {
	normalized := append([]string(nil), sources...)
	sort.Strings(normalized)
	seed := strings.Join([]string{
		account,
		o.Before.UTC().Format(time.RFC3339),
		fmt.Sprint(o.KeepUnread),
		fmt.Sprint(o.KeepFlagged),
		strings.Join(normalized, "\x00"),
	}, "|")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

// OpenJournal loads the journal for this key if one exists, or starts a new
// one. The returned journal is never nil; a load failure is reported so the
// caller can mention it without failing the run.
func OpenJournal(dir, key string, args []string, now time.Time) (*Journal, error) {
	path := filepath.Join(dir, "run-"+key+".json")
	j := &Journal{
		Version:   journalVersion,
		Key:       key,
		Args:      args,
		StartedAt: now,
		UpdatedAt: now,
		Moved:     map[string]int{},
		path:      path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return j, nil
		}
		return j, fmt.Errorf("reading %s: %w", path, err)
	}

	var prev Journal
	if err := json.Unmarshal(data, &prev); err != nil {
		return j, fmt.Errorf("ignoring unreadable journal %s: %w", path, err)
	}
	if prev.Version != journalVersion || prev.Moved == nil {
		return j, nil
	}
	prev.path = path
	prev.Args = args
	prev.UpdatedAt = now
	return &prev, nil
}

// Path is where the journal is stored.
func (j *Journal) Path() string { return j.path }

// PriorMoved is the number of messages recorded by earlier runs of the same
// command, which is what makes a resume message possible.
func (j *Journal) PriorMoved() int {
	total := 0
	for _, n := range j.Moved {
		total += n
	}
	return total
}

// RecordBatch adds a completed batch.
func (j *Journal) RecordBatch(mailbox string, moved int) {
	if j.Moved == nil {
		j.Moved = map[string]int{}
	}
	j.Moved[mailbox] += moved
	j.Batches++
	j.UpdatedAt = time.Now()
}

// SaveBestEffort writes the journal, retaining any error for later reporting.
func (j *Journal) SaveBestEffort() {
	if err := j.Save(); err != nil && j.saveErr == nil {
		j.saveErr = err
	}
}

// SaveErr returns the first write failure, if any.
func (j *Journal) SaveErr() error { return j.saveErr }

// Save writes the journal atomically: a temp file in the same directory,
// then a rename, so an interrupted write cannot leave a half-written journal.
func (j *Journal) Save() error {
	if j.path == "" {
		return nil
	}
	dir := filepath.Dir(j.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "run-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp journal: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, j.path); err != nil {
		return fmt.Errorf("renaming into %s: %w", j.path, err)
	}
	return nil
}

// Finish stamps the completion time and writes.
func (j *Journal) Finish(now time.Time) {
	j.UpdatedAt = now
	j.SaveBestEffort()
}
