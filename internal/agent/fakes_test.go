package agent

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/jenish0908/assetflow-be/internal/service"
)

// fakeAssets is an in-memory AssetService that logs every call, with hooks
// so tests can observe or block calls.
type fakeAssets struct {
	mu     sync.Mutex
	byTag  map[string]service.AssetWithAllocations
	nextID int64
	events []string

	onGet func(ctx context.Context, tag string) error // runs before GetByTag returns
}

func newFakeAssets(tags ...string) *fakeAssets {
	f := &fakeAssets{byTag: map[string]service.AssetWithAllocations{}}
	for _, tag := range tags {
		f.nextID++
		f.byTag[tag] = service.AssetWithAllocations{
			Asset:       service.Asset{ID: f.nextID, Tag: tag, Name: "Asset " + tag, Category: "laptop", Status: service.StatusAvailable},
			Allocations: []service.Allocation{},
		}
	}
	return f
}

func (f *fakeAssets) log(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeAssets) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeAssets) GetByTag(ctx context.Context, tag string) (service.AssetWithAllocations, error) {
	f.log("get:" + tag + ":start")
	defer f.log("get:" + tag + ":end")
	if f.onGet != nil {
		if err := f.onGet(ctx, tag); err != nil {
			return service.AssetWithAllocations{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.byTag[tag]
	if !ok {
		return a, service.NewError(service.ErrNotFound, "asset with tag %q not found", tag)
	}
	return a, nil
}

func (f *fakeAssets) Create(_ context.Context, in service.NewAsset) (service.Asset, error) {
	f.log("create:" + in.Tag)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	a := service.Asset{ID: f.nextID, Tag: in.Tag, Name: in.Name, Category: in.Category, Status: service.StatusAvailable}
	f.byTag[in.Tag] = service.AssetWithAllocations{Asset: a, Allocations: []service.Allocation{}}
	return a, nil
}

func (f *fakeAssets) Update(_ context.Context, id int64, _ service.AssetPatch) (service.Asset, error) {
	f.log("update")
	return service.Asset{ID: id}, nil
}

func (f *fakeAssets) Allocate(_ context.Context, assetID, userID int64) (service.Allocation, error) {
	f.log("allocate:start")
	defer f.log("allocate:end")
	f.mu.Lock()
	defer f.mu.Unlock()
	for tag, a := range f.byTag {
		if a.ID == assetID {
			a.Status = service.StatusAllocated
			f.byTag[tag] = a
		}
	}
	return service.Allocation{ID: 1, AssetID: assetID, UserID: userID}, nil
}

func (f *fakeAssets) UpdateStatus(_ context.Context, id int64, s service.AssetStatus) (service.Asset, error) {
	f.log("status:" + string(s))
	return service.Asset{ID: id, Status: s}, nil
}

type fakeUsers struct {
	mu      sync.Mutex
	users   []service.User
	created []service.CreateUserInput
}

func (f *fakeUsers) Find(_ context.Context, q string) ([]service.User, error) {
	var out []service.User
	for _, u := range f.users {
		if strings.EqualFold(u.Email, q) || strings.EqualFold(u.Name, q) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeUsers) Create(_ context.Context, in service.CreateUserInput) (service.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	return service.User{ID: 99, Email: in.Email, Name: in.Name, Role: in.Role}, nil
}

// scriptedLLM returns its replies in order and records every request.
type scriptedLLM struct {
	mu       sync.Mutex
	replies  []Message
	requests []ChatRequest
}

func (s *scriptedLLM) Chat(_ context.Context, req ChatRequest) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.replies) == 0 {
		return Message{Role: RoleAssistant, Content: "done"}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

// callsThenDone scripts one turn of tool calls followed by a text answer.
func callsThenDone(calls ...ToolCall) *scriptedLLM {
	for i := range calls {
		calls[i].ID = "call_" + string(rune('a'+i))
	}
	return &scriptedLLM{replies: []Message{{Role: RoleAssistant, ToolCalls: calls}, {Role: RoleAssistant, Content: "done"}}}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (f *fakeUsers) Get(_ context.Context, id int64) (service.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return service.User{}, service.NewError(service.ErrNotFound, "user %d not found", id)
}
