package api

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	Starting  = "starting"
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
)

type Submission struct {
	ID        string   `json:"id,omitempty"`
	Script    string   `json:"script"`
	Args      []string `json:"args,omitempty"`
	OutputDir string   `json:"output_dir,omitempty"`
}

type Job struct {
	ID          string     `json:"id"`
	Script      string     `json:"script"`
	OutputDir   string     `json:"output_dir,omitempty"`
	State       string     `json:"state"`
	SubmittedAt time.Time  `json:"submitted_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	StataRC     *int       `json:"stata_rc,omitempty"`
	ProcessExit *int       `json:"process_exit_code,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (j Job) Active() bool {
	return j.State == Starting || j.State == Running
}

type LogChunk struct {
	Data       []byte `json:"data,omitempty"`
	NextOffset int64  `json:"next_offset"`
	Complete   bool   `json:"complete"`
}

type Error struct {
	Error string `json:"error"`
}

func NewID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'f' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}
