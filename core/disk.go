package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Each agent has a disk cap: the most its work folder may hold. The machine that runs a shift
// measures the folder once claude ends; the brief of the agent's next shift says what it held,
// and a folder over the cap gets the agent a note to free its own unused files before anything
// else, or to ask the human for more.

// Disk is an agent's work folder as last measured.
type Disk struct {
	Bytes int64     `json:"bytes"`
	At    time.Time `json:"at"`
}

func (c *Config) diskFile(agent string) string { return c.Path(filepath.Join(diskDir, agent+".json")) }

// keepDisk records what the agent's work folder held after its shift.
func (t *Team) keepDisk(agent string, d Disk) error {
	if err := os.MkdirAll(t.Path(diskDir), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(d)
	tmp := t.diskFile(agent) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.diskFile(agent))
}

// DiskOf is what the agent's work folder held when it was last measured; false: never.
func (c *Config) DiskOf(agent string) (Disk, bool) {
	var d Disk
	b, err := os.ReadFile(c.diskFile(agent))
	if err != nil || json.Unmarshal(b, &d) != nil || d.At.IsZero() {
		return Disk{}, false
	}
	return d, true
}

// overCap says whether a folder of bytes holds more than a cap of capGB; a cap of 0 is none.
func overCap(bytes int64, capGB int) bool { return capGB > 0 && bytes > int64(capGB)*1e9 }

// GB is a size in bytes as people read it: 12.3 GB.
func GB(bytes int64) string { return fmt.Sprintf("%.1f GB", float64(bytes)/1e9) }

// diskText is the brief's {disk}: what the agent's work folder held and its cap.
func (t *Team) diskText(a *Agent) string {
	held := "your work folder has not been measured yet"
	if d, ok := t.DiskOf(a.Name); ok {
		held = fmt.Sprintf("your work folder held %s when your last shift ended (%s)", GB(d.Bytes), d.At.Local().Format("Mon 15:04"))
	}
	if a.DiskGB == 0 {
		return held + "; you have no cap"
	}
	return fmt.Sprintf("%s; your cap is %d GB", held, a.DiskGB)
}

// overCapNote is the organizer's note to an agent whose folder holds more than its cap.
func overCapNote(a *Agent, d Disk) string {
	return fmt.Sprintf("your work folder held %s when your last shift ended, over your cap of %d GB. Before anything else, free space: delete what you made and no longer need, in your work folder only, until it holds less than the cap. If you cannot without deleting what you still need, ask the human for a higher cap, saying how much and why, and do no other work until the folder is under the cap or the human has raised it.",
		GB(d.Bytes), a.DiskGB)
}
