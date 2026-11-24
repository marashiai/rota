package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"

	"rota/core"
)

// The pages read aloud and take dictation (voice.js): with ElevenLabs through rota when its key
// may, else with the browser's own. The key never leaves this machine.

// voiceTag loads voice.js on a page that is not the app: a review page, a report. It carries
// the pages' token, which the app's page has on its body.
func (s *site) voiceTag() template.HTML {
	return template.HTML(`<script src="/voice.js?v=` + assets["voice.js"].hash + `" data-token="` + template.HTMLEscapeString(s.token) + `"></script>`)
}

// voiceJSON says who reads aloud and who takes dictation, for voice.js.
func (s *site) voiceJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, r, struct {
		core.VoiceUse
		Max   int     `json:"max"`
		Voice string  `json:"voice"` // the ElevenLabs voice that reads
		Speed float64 `json:"speed"` // voice_speed, for the browser's voice too
	}{t.Voice(r.Context()), core.SayMax, t.VoiceID, t.VoiceSpeed})
}

// voicesJSON lists the ElevenLabs voices to choose from in Settings.
func (s *site) voicesJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list, why := t.Voices(r.Context())
	writeJSON(w, r, struct {
		Voices []core.VoiceChoice `json:"voices"`
		OwnWhy string             `json:"ownWhy,omitempty"` // why the account's own voices are not listed
	}{list, why})
}

// voiceSay reads a text aloud with ElevenLabs, as MP3; when ElevenLabs refuses, the page reads it
// with the browser's voice.
func (s *site) voiceSay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	if !s.voiceAllowed(r) {
		voiceError(w, http.StatusForbidden, "refused")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		voiceError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := &firstWrite{w: w}
	if err := t.ReadAloud(r.Context(), r.FormValue("text"), out); err != nil && !out.wrote {
		voiceError(w, http.StatusBadGateway, err.Error())
	}
}

// firstWrite says it is audio when the first of it comes, so a refusal before it is an error.
type firstWrite struct {
	w     http.ResponseWriter
	wrote bool
}

func (f *firstWrite) Write(b []byte) (int, error) {
	if !f.wrote {
		f.wrote = true
		f.w.Header().Set("Content-Type", "audio/mpeg")
		f.w.Header().Set("Cache-Control", "no-store")
	}
	return f.w.Write(b)
}

// voiceListen writes down what a recording says, with ElevenLabs.
func (s *site) voiceListen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	if !s.voiceAllowed(r) { // before the recording is read
		voiceError(w, http.StatusForbidden, "refused")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, core.ListenMax+64<<10)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		voiceError(w, http.StatusBadRequest, "the recording is too long or unreadable")
		return
	}
	f, head, err := r.FormFile("audio")
	if err != nil {
		voiceError(w, http.StatusBadRequest, "no recording")
		return
	}
	defer f.Close()
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		voiceError(w, http.StatusInternalServerError, err.Error())
		return
	}
	text, err := t.Transcribe(r.Context(), f, filepath.Base(head.Filename), head.Header.Get("Content-Type"))
	if err != nil {
		voiceError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, r, map[string]string{"text": text})
}

// voiceFell hears from a page that it used the browser's own voice or dictation, and why.
func (s *site) voiceFell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.voiceAllowed(r) {
		voiceError(w, http.StatusForbidden, "refused")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	core.FellBack(r.FormValue("why"), device(r.UserAgent()))
	w.WriteHeader(http.StatusNoContent)
}

// device names what a browser runs on, as far as the log needs it.
func device(ua string) string {
	on := "a computer"
	for _, d := range []string{"iPhone", "iPad", "Android", "Macintosh", "Windows", "Linux"} {
		if strings.Contains(ua, d) {
			on = strings.Replace(d, "Macintosh", "a Mac", 1)
			break
		}
	}
	for _, b := range [][2]string{{"CriOS", "Chrome"}, {"FxiOS", "Firefox"}, {"Edg/", "Edge"}, {"Chrome/", "Chrome"}, {"Firefox/", "Firefox"}, {"Safari/", "Safari"}} {
		if strings.Contains(ua, b[0]) {
			return b[1] + " on " + on
		}
	}
	return on
}

// voiceAllowed takes only what these pages ask, with their token in a header: it is checked
// before anything is read.
func (s *site) voiceAllowed(r *http.Request) bool {
	return fromHere(r) && r.Header.Get("X-Rota-Token") == s.token
}

func voiceError(w http.ResponseWriter, status int, why string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": strings.TrimSpace(why)})
}
