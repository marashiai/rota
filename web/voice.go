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
		Max int `json:"max"`
	}{t.Voice(r.Context()), core.SayMax})
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
