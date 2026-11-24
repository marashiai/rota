package core

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The starter team folder: the brief, an example secrets file, and rota.conf's starter
// values; the rest of rota.conf is the declarations' defaults. It ships no agents: each one
// is a Markdown file you add to agents/.
//
//go:embed templates
var templates embed.FS

// DefaultBrief is the shift brief, the prompt of every shift, for a team folder without a
// brief.md of its own. Placeholders: {agent} {date} {time} {root} {work} {repos} {host}
// {journal} {inbox} {agents} {weekly} (the agent's fair share of the team pool) {model}
// {run_minutes} {shift} (how long its shift lasts: at most run_minutes, or with extend while it
// makes real progress) {questions} (its questions to the human that are still open) {pages} (the
// pages' address) {labels} (the team's own labels, from the labels setting) {disk} (what its
// work folder held after its last shift, and its disk cap) {plan} (the open issues in the order
// an agent gave last with rota work-order, each with why). A role takes them too.
var DefaultBrief = func() string { b, _ := templates.ReadFile("templates/brief.md"); return string(b) }()

// Init creates the team folder from the starter files. Existing files are never overwritten.
func Init(root string) error {
	var created, kept int
	err := fs.WalkDir(templates, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("templates", p)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if _, err := os.Stat(dst); err == nil {
			kept++
			return nil
		}
		b, err := templates.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "rota.conf" { // the starter values, documented with every other setting
			values, err := parseKV(string(b))
			if err != nil {
				return err
			}
			b = []byte(ConfFile(values))
		}
		created++
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return err
	}
	for _, dir := range []string{agentsDir, journalDir, inboxDir, workDir, logDir, reportsDir, evidenceDir, secretsDir} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
	}
	os.Chmod(filepath.Join(root, secretsDir), 0o700)
	fmt.Printf("team folder %s: %d files created, %d kept\n", root, created, kept)
	fmt.Println("next: add an agent as agents/<name>.md there, edit rota.conf, then: rota status, rota enable <agent>, rota start")
	return nil
}
