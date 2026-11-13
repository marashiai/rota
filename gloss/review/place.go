package review

// LineKey names a line of a diff.
type LineKey struct {
	Path string
	Line int
	Old  bool
}

// Placed is where a view's threads show in its diff: the whole change's, or one commit's own.
type Placed struct {
	Whole []*Thread             // on the whole change or the whole commit, and what the diff cannot show
	Files map[string][]*Thread  // on a whole file, or on a line the diff does not show
	Lines map[LineKey][]*Thread // on a line of the diff
	Count int                   // the view's threads
}

func (p *Placed) put(t *Thread, f File, ok bool, k LineKey, found bool) {
	p.Count++
	switch {
	case t.Path == "" || !ok:
		p.Whole = append(p.Whole, t)
	case found:
		p.Lines[k] = append(p.Lines[k], t)
	default:
		p.Files[t.Path] = append(p.Files[t.Path], t)
	}
}

// OnFile counts the threads placed on the file: on it, and on its lines.
func (p Placed) OnFile(path string) int {
	n := len(p.Files[path])
	for k, ts := range p.Lines {
		if k.Path == path {
			n += len(ts)
		}
	}
	return n
}

// Place sorts the threads of commit i by where they show in files, its own diff.
func (rv *Review) Place(i int, files []File) Placed {
	p := Placed{Files: map[string][]*Thread{}, Lines: map[LineKey][]*Thread{}}
	sha := rv.Commits[i].SHA
	for _, t := range rv.Threads {
		if t.Commit != sha {
			continue
		}
		f, ok := fileIn(files, t.Path)
		k := LineKey{t.Path, t.Line, t.Old}
		found := false
		if ok && t.Line != 0 {
			_, _, found = f.Find(t.Line, t.Old)
		}
		p.put(t, f, ok, k, found)
	}
	return p
}

// PlaceWhole sorts the threads by where they show in files, the whole change's diff from the base
// to the head: the notes on the whole change, and every note on a line or a file, whichever
// commit it was written on, where GitHub tracks it to in the diff as it is now. Notes on a whole
// commit stay in that commit, unless the commit is gone.
func (rv *Review) PlaceWhole(files []File) Placed {
	p := Placed{Files: map[string][]*Thread{}, Lines: map[LineKey][]*Thread{}}
	for _, t := range rv.Threads {
		if t.Path == "" && t.Commit != "" && rv.CommitNumber(t.Commit) > 0 {
			continue
		}
		f, ok := fileIn(files, t.Path)
		k, found := LineKey{}, false
		if ok {
			k, found = t.wholeLine(rv, f)
		}
		p.put(t, f, ok, k, found)
	}
	return p
}

// wholeLine is the line of f, in the whole change's diff, that the thread shows on: where GitHub
// tracks it, or its own line when it was written on the diff as it is now. A thread whose line a
// later commit changed, or that GitHub does not track, shows on the file.
func (t *Thread) wholeLine(rv *Review, f File) (LineKey, bool) {
	var k LineKey
	switch {
	case t.Outdated || t.Line == 0:
		return k, false
	case t.HeadLine > 0:
		k = LineKey{t.Path, t.HeadLine, t.HeadOld}
	case t.Commit == "" && t.Head == rv.Head, t.Commit == rv.Head && !t.Old:
		k = LineKey{t.Path, t.Line, t.Old} // the head's new side is the diff's new side
	default:
		return k, false
	}
	_, _, ok := f.Find(k.Line, k.Old)
	return k, ok
}

// Overall is the threads on the whole change itself, and those on commits no longer in the pull
// request (after a force-push).
func (rv *Review) Overall() []*Thread {
	var out []*Thread
	for _, t := range rv.Threads {
		if t.Path == "" && (t.Commit == "" || rv.CommitNumber(t.Commit) == 0) {
			out = append(out, t)
		}
	}
	return out
}

// WholeThreads counts the threads on the whole change: on it, and on its diff.
func (rv *Review) WholeThreads() (n int) {
	for _, t := range rv.Threads {
		if t.Commit == "" {
			n++
		}
	}
	return n
}

// CommitThreads counts the threads on commit i.
func (rv *Review) CommitThreads(i int) (n int) {
	for _, t := range rv.Threads {
		if t.Commit == rv.Commits[i].SHA {
			n++
		}
	}
	return n
}

// CommitNumber is the commit's number (1 is the first), 0 when the pull request has no such
// commit.
func (rv *Review) CommitNumber(sha string) int {
	if sha == "" {
		return 0
	}
	for i, c := range rv.Commits {
		if c.SHA == sha {
			return i + 1
		}
	}
	return 0
}
