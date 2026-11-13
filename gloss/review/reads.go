package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Read is what the reviewer has read of one pull request: every note written by All, and each
// thread's notes written by its time in Threads. GitHub keeps no such mark for a person, so gloss
// keeps it on this machine (Reads).
type Read struct {
	All     time.Time            `json:"all,omitzero"`
	Threads map[string]time.Time `json:"threads,omitempty"`
	At      time.Time            `json:"at,omitzero"` // when it last changed
}

// Has says whether the reviewer has read note n of thread t. Their own notes and drafts they
// have; not a PR comment from their account, which an agent may have written.
func (r Read) Has(rv *Review, t *Thread, n *Note) bool {
	if n.Draft || rv.Mine(n) && !n.PRComment {
		return true
	}
	upto := r.Threads[t.ID]
	if r.All.After(upto) {
		upto = r.All
	}
	return !upto.IsZero() && !n.Created.After(upto)
}

// Folded says whether t shows folded: the reviewer has read every note in it, and it holds no
// draft of theirs.
func (r Read) Folded(rv *Review, t *Thread) bool {
	for _, n := range t.Notes {
		if n.Draft || !r.Has(rv, t, n) {
			return false
		}
	}
	return true
}

// New counts the notes in ts the reviewer has not read.
func (r Read) New(rv *Review, ts []*Thread) (n int) {
	for _, t := range ts {
		for _, x := range t.Notes {
			if !r.Has(rv, t, x) {
				n++
			}
		}
	}
	return n
}

// Mark records that the reviewer read t's notes up to upto.
func (r *Read) Mark(t *Thread, upto time.Time) {
	if !upto.After(r.Threads[t.ID]) {
		return
	}
	if r.Threads == nil {
		r.Threads = map[string]time.Time{}
	}
	r.Threads[t.ID] = upto
}

// MarkAll records that the reviewer read every note of rv.
func (r *Read) MarkAll(rv *Review) {
	for _, t := range rv.Threads {
		if l := Latest(t); l.After(r.All) {
			r.All = l
		}
	}
}

// Add takes in what o has read too.
func (r *Read) Add(o Read) {
	if o.All.After(r.All) {
		r.All = o.All
	}
	for id, at := range o.Threads {
		r.Mark(&Thread{ID: id}, at)
	}
}

// Clone is a copy that changes apart from r.
func (r Read) Clone() Read {
	c := r
	c.Threads = nil
	c.Add(Read{Threads: r.Threads})
	return c
}

// Latest is when t's latest sent note was written.
func Latest(t *Thread) (at time.Time) {
	for _, n := range t.Notes {
		if !n.Draft && n.Created.After(at) {
			at = n.Created
		}
	}
	return at
}

// Reads keeps what the reviewer has read of each pull request in a file, so the console and the
// pages, and every device that opens the pages, share it. A nil Reads keeps nothing.
type Reads struct {
	path string
	mu   sync.Mutex
}

// NewReads keeps them in the file at path.
func NewReads(path string) *Reads { return &Reads{path: path} }

// DefaultReads keeps them in gloss/read.json in the user's config folder; nil when it has none.
func DefaultReads() *Reads {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	return NewReads(filepath.Join(dir, "gloss", "read.json"))
}

// forget is how long a pull request's marks stay after they last changed: by then it is merged
// or closed.
const forget = 180 * 24 * time.Hour

// errBroken is a file that is not what Mark writes.
var errBroken = errors.New("not gloss's marks")

func (s *Reads) all() (map[string]Read, error) {
	all := map[string]Read{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return all, nil
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", s.path, errBroken, err)
	}
	return all, nil
}

// Get is what the reviewer has read of the pull request (owner/name#n).
func (s *Reads) Get(pr string) (Read, error) {
	if s == nil {
		return Read{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.all()
	return all[pr], err
}

// Mark adds what r has read to the pull request's marks. Each write reads the file first and
// replaces it whole, so two programs that mark at the same moment lose at most a mark: a note
// that then shows as new once more.
func (s *Reads) Mark(pr string, r Read) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.all()
	if errors.Is(err, errBroken) { // kept beside, and started again
		if err := os.Rename(s.path, s.path+".broken"); err != nil {
			return err
		}
		all, err = map[string]Read{}, nil
	}
	if err != nil {
		return err
	}
	cur := all[pr]
	before := cur.Clone()
	cur.Add(r)
	if cur.All.Equal(before.All) && len(cur.Threads) == len(before.Threads) && sameMarks(cur.Threads, before.Threads) {
		return nil
	}
	now := time.Now()
	cur.At = now
	all[pr] = cur
	for k, v := range all {
		if now.Sub(v.At) > forget {
			delete(all, k)
		}
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".read-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func sameMarks(a, b map[string]time.Time) bool {
	for k, v := range a {
		if !v.Equal(b[k]) {
			return false
		}
	}
	return true
}
