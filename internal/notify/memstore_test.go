package notify

import (
	"context"
	"slices"
	"sync"
	"time"
)

// memStore is an in-memory Store with the same lease and fencing rules as
// the SQL version, so worker behaviour can be tested deterministically.
type memStore struct {
	mu       sync.Mutex
	jobs     []*memJob
	claimErr error
}

type memJob struct {
	Job
	status      string
	runAt       time.Time
	lockedBy    string
	lockedUntil time.Time
	lastErr     string
}

func newMemStore(n, maxAttempts int) *memStore {
	s := &memStore{}
	for i := range n {
		s.jobs = append(s.jobs, &memJob{
			Job:    Job{ID: int64(i + 1), Kind: "test", MaxAttempts: maxAttempts},
			status: "pending",
		})
	}
	return s
}

func (s *memStore) ClaimJobs(_ context.Context, workerID string, kinds []string, limit int, lease time.Duration) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	now := time.Now()
	var out []Job
	for _, j := range s.jobs {
		if len(out) == limit {
			break
		}
		runnable := (j.status == "pending" && !j.runAt.After(now)) ||
			(j.status == "running" && j.lockedUntil.Before(now))
		if !runnable || !slices.Contains(kinds, j.Kind) {
			continue
		}
		j.status, j.lockedBy, j.lockedUntil = "running", workerID, now.Add(lease)
		j.Attempts++
		out = append(out, j.Job)
	}
	return out, nil
}

// owned returns the job if workerID still holds its lease (the fence).
func (s *memStore) owned(id int64, workerID string) *memJob {
	for _, j := range s.jobs {
		if j.ID == id && j.status == "running" && j.lockedBy == workerID {
			return j
		}
	}
	return nil
}

func (s *memStore) CompleteJob(_ context.Context, id int64, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.owned(id, workerID); j != nil {
		j.status, j.lockedBy = "done", ""
	}
	return nil
}

func (s *memStore) RetryJob(_ context.Context, id int64, workerID string, runAt time.Time, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.owned(id, workerID); j != nil {
		j.status, j.lockedBy, j.runAt, j.lastErr = "pending", "", runAt, lastErr
	}
	return nil
}

func (s *memStore) DeadLetterJob(_ context.Context, id int64, workerID string, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.owned(id, workerID); j != nil {
		j.status, j.lockedBy, j.lastErr = "dead", "", lastErr
	}
	return nil
}

func (s *memStore) ReleaseJobs(_ context.Context, ids []int64, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if j := s.owned(id, workerID); j != nil {
			j.status, j.lockedBy = "pending", ""
			j.Attempts--
		}
	}
	return nil
}

func (s *memStore) count(status string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.status == status {
			n++
		}
	}
	return n
}

func (s *memStore) snapshot() []memJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]memJob, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = *j
	}
	return out
}
