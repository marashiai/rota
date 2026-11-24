package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeEleven answers as ElevenLabs does: it reads aloud and writes down for the keys that may,
// and refuses the others as ElevenLabs refuses them.
type fakeEleven struct {
	say, listen     map[string]bool // the keys that may
	sayN, listenN   atomic.Int32
	voice, model    string
	file, fileModel string
	verbatim        string
	speed           float64
	list            map[string]bool // the keys that may list the account's voices
}

func (f *fakeEleven) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("xi-api-key")
		refuse := func(perm string) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"detail": map[string]string{"status": "missing_permissions",
				"message": "The API key you used is missing the permission " + perm + " to execute this operation."}})
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			f.sayN.Add(1)
			if !f.say[key] {
				refuse("text_to_speech")
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["text"] == "too many" {
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"detail":{"status":"too_many_concurrent_requests","message":"Too many concurrent requests"}}`)
				return
			}
			f.voice, f.model, f.speed = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/"), "/stream"), body["model_id"].(string), 0
			if vs, ok := body["voice_settings"].(map[string]any); ok {
				f.speed, _ = vs["speed"].(float64)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, "ID3 mp3 of: "+body["text"].(string))
		case r.URL.Path == "/v2/voices":
			if !f.list[key] {
				refuse("voices_read")
				return
			}
			io.WriteString(w, `{"voices": [{"voice_id": "GEORGE", "name": "George", "category": "premade"},
				{"voice_id": "MINE", "name": "My clone", "category": "cloned", "labels": {"gender": "female", "accent": "american"}},
				{"voice_id": "QtY3JBOUKEB5xzrRfOKc", "name": "Maisie", "category": "professional"}]}`)
		case r.URL.Path == "/v1/speech-to-text":
			f.listenN.Add(1)
			if !f.listen[key] {
				refuse("speech_to_text")
				return
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("no file: %v", err)
				return
			}
			b, _ := io.ReadAll(file)
			f.file, f.fileModel, f.verbatim = string(b), r.FormValue("model_id"), r.FormValue("no_verbatim")
			json.NewEncoder(w).Encode(map[string]any{"text": " Merge the five pull requests. ", "language_code": "en"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	was := ElevenAPI
	ElevenAPI = srv.URL
	t.Cleanup(func() { ElevenAPI = was })
}

func TestTheVoiceIsElevenLabsForWhatItsKeyMay(t *testing.T) {
	root, team := mailTeam(t)
	f := &fakeEleven{say: map[string]bool{"sk_long_key_both": true, "sk_say": true}, listen: map[string]bool{"sk_long_key_both": true}}
	f.serve(t)
	ctx := context.Background()

	if v := team.Voice(ctx); v.Key || v.Say || v.Listen || f.sayN.Load()+f.listenN.Load() != 0 {
		t.Fatalf("no key: the browser's own, and ElevenLabs is not asked: %+v", v)
	}
	if err := team.SetVoiceKey("sk say"); err == nil {
		t.Fatal("a key of two words is kept")
	}
	if err := team.SetVoiceKey(" sk_say\n"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "secrets", "elevenlabs.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key is kept for this user only: %v %v", fi, err)
	}
	if settings, _ := os.ReadFile(filepath.Join(root, "rota.conf")); strings.Contains(string(settings), "sk_say") {
		t.Fatal("the key is in rota.conf")
	}
	v := team.Voice(ctx)
	if v.KeyEnd != "" {
		t.Fatalf("a short key shows none of itself: %q", v.KeyEnd)
	}
	if !v.Key || !v.Say || v.Listen || !strings.Contains(v.ListenWhy, "missing the permission speech_to_text") || v.SayWhy != "" {
		t.Fatalf("a key that may only read aloud: %+v", v)
	}
	if f.voice != "g7LVvkPWALzPxOQbF6OE" || f.model != sayModel {
		t.Fatalf("the voice and model asked for: %q %q", f.voice, f.model)
	}
	asked := f.sayN.Load() + f.listenN.Load()
	if team.Voice(ctx); f.sayN.Load()+f.listenN.Load() != asked {
		t.Fatal("ElevenLabs is asked again at once")
	}

	var mp3 bytes.Buffer
	if err := team.ReadAloud(ctx, "The plan, in order.", &mp3); err != nil || mp3.String() != "ID3 mp3 of: The plan, in order." {
		t.Fatalf("read aloud: %q %v", mp3.String(), err)
	}
	if err := team.ReadAloud(ctx, strings.Repeat("a", SayMax+1), io.Discard); err == nil {
		t.Fatal("a text longer than one reading is read")
	}
	if err := team.ReadAloud(ctx, "too many", io.Discard); err == nil || !strings.Contains(err.Error(), "Too many concurrent requests") {
		t.Fatalf("ElevenLabs busy: %v", err)
	}
	if v := team.Voice(ctx); !v.Say {
		t.Fatal("ElevenLabs busy once: the pages no longer read with it")
	}
	if _, err := team.Transcribe(ctx, strings.NewReader("webm"), "speech.webm", "audio/webm"); err == nil {
		t.Fatal("a key that may not write down writes down")
	}

	if err := team.SetVoiceKey("sk_long_key_both"); err != nil {
		t.Fatal(err)
	}
	if v := team.Voice(ctx); !v.Say || !v.Listen || v.KeyEnd != "both" {
		t.Fatalf("another key is asked about at once, and shows its end: %+v", v)
	}
	text, err := team.Transcribe(ctx, strings.NewReader("webm audio"), "speech.webm", "audio/webm")
	if err != nil || text != "Merge the five pull requests." || f.file != "webm audio" || f.fileModel != listenModel || f.verbatim != "true" {
		t.Fatalf("written down: %q %v (%q %q)", text, err, f.file, f.fileModel)
	}

	// the key loses a permission at ElevenLabs: the pages use the browser's own from then on
	delete(f.say, "sk_long_key_both")
	if err := team.ReadAloud(ctx, "Hello", io.Discard); err == nil || !strings.Contains(err.Error(), "text_to_speech") {
		t.Fatalf("a refused reading: %v", err)
	}
	if v := team.Voice(ctx); v.Say || !v.Listen || !strings.Contains(v.SayWhy, "text_to_speech") {
		t.Fatalf("after ElevenLabs refused to read: %+v", v)
	}

	if err := team.SetVoiceKey(""); err != nil {
		t.Fatal(err)
	}
	if err := team.SetVoiceKey(""); err != nil {
		t.Fatal("removing no key fails:", err)
	}
	if v := team.Voice(ctx); v.Key || v.Say || v.Listen {
		t.Fatalf("the key removed: %+v", v)
	}
}

func TestSilenceIsAWavFile(t *testing.T) {
	b := silence()
	if len(b) != 44+16000 || string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("not a WAV file: %d %q", len(b), b[:44])
	}
}

// ElevenLabs reads with v4 at its own speed, whatever the speed chosen: the pages change it. The
// voices to choose from are those ElevenLabs keeps for good, and the account's own when the key
// may list them.
func TestTheVoiceReadsAtItsSpeedAndListsItsChoices(t *testing.T) {
	_, team := mailTeam(t)
	f := &fakeEleven{say: map[string]bool{"sk_long_key_both": true}, listen: map[string]bool{}, list: map[string]bool{}}
	f.serve(t)
	ctx := context.Background()
	if err := team.SetVoiceKey("sk_long_key_both"); err != nil {
		t.Fatal(err)
	}
	for _, speed := range []float64{1, 1.35, 0.6} {
		team.VoiceSpeed = speed
		if err := team.ReadAloud(ctx, "Hello", io.Discard); err != nil || f.model != sayModel || f.speed != 0 {
			t.Errorf("speed %v: %s at %v, want %s at its own speed (%v)", speed, f.model, f.speed, sayModel, err)
		}
	}
	list, why := team.Voices(ctx)
	if len(list) != len(infinityVoices) || !strings.Contains(why, "voices_read") {
		t.Fatalf("a key that may not list: ElevenLabs's own, and why: %d %q", len(list), why)
	}
	f.list["sk_long_key_both"] = true
	list, why = team.Voices(ctx)
	if why != "" || len(list) != len(infinityVoices)+1 || list[0].ID != "MINE" || !list[0].Own || list[0].About != "female, american" {
		t.Fatalf("the account's own first, no default voice, no voice twice: %q %+v", why, list[:2])
	}
}
