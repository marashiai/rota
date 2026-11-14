package core

import (
	"errors"
	"reflect"
	"testing"
)

func TestTheMenuBarReopensAsAnAppAndHandsItsOrganizerOver(t *testing.T) {
	old := reopen
	defer func() { reopen = old }()
	var got []string
	reopen = func(args []string) error { got = args; return nil }
	s := &SelfUpdate{&selfWatch{exe: "/Applications/rota.app/Contents/MacOS/rota"}}
	if err := s.Reopen("/Applications/rota.app", "ROTA_TRAY_ORGANIZER=42"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"-n", "-a", "/Applications/rota.app", "--env", "ROTA_TRAY_ORGANIZER=42"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("open %q, want %q: a new instance of the app, with the organizer to take over", got, want)
	}
	if !s.w.failedAt.IsZero() {
		t.Fatal("an open that worked counts as failed")
	}
	reopen = func([]string) error { return errors.New("no such application") }
	if s.Reopen("/Applications/rota.app") == nil {
		t.Fatal("an open that failed says it worked: the menu would quit with no new one")
	}
	if s.w.failedAt.IsZero() {
		t.Fatal("a new rota that would not open is tried again at once, every two seconds")
	}
}
