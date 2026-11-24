package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"rota/core"
)

// The pages read aloud and take dictation with ElevenLabs through rota, which keeps the key; a
// key that may not, or none, leaves both to the browser.
func TestThePagesSpeakAndListenThroughRota(t *testing.T) {
	root, srv := team(t)
	eleven := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("xi-api-key") != "sk_test" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			io.WriteString(w, "mp3:"+body["text"])
		case r.URL.Path == "/v1/speech-to-text":
			f, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			b, _ := io.ReadAll(f)
			json.NewEncoder(w).Encode(map[string]string{"text": "heard " + string(b)})
		}
	}))
	defer eleven.Close()
	was := core.ElevenAPI
	core.ElevenAPI = eleven.URL
	defer func() { core.ElevenAPI = was }()

	app := get(t, srv, "/")
	if !strings.Contains(app, `<script src="/voice.js?v=`) {
		t.Fatal("the app loads voice.js")
	}
	if js := get(t, srv, "/voice.js"); !strings.Contains(js, "window.rotaVoice") || !strings.Contains(js, "speechSynthesis") || !strings.Contains(js, "SpeechRecognition") {
		t.Fatal("voice.js falls back to the browser's own voice and dictation")
	}
	var resp *http.Response
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(app)[1]
	act := func(form url.Values) {
		t.Helper()
		form.Set("token", token)
		resp, err := http.PostForm(srv.URL+"/api/act", form)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v: %v %v", form, resp.Status, err)
		}
		resp.Body.Close()
	}
	voice := func() core.VoiceUse {
		var v core.VoiceUse
		json.Unmarshal([]byte(get(t, srv, "/api/voice")), &v)
		return v
	}
	if v := voice(); v.Key || v.Say || v.Listen {
		t.Fatalf("no key: %+v", v)
	}

	act(url.Values{"action": {"voice-key"}, "text": {"sk_wrong"}})
	if v := voice(); !v.Key || v.Say || v.Listen || v.SayWhy != "ElevenLabs: Invalid API key" {
		t.Fatalf("a key ElevenLabs refuses: %+v", v)
	}
	act(url.Values{"action": {"voice-key"}, "text": {"sk_test"}})
	if b, _ := os.ReadFile(filepath.Join(root, "secrets", "elevenlabs.key")); string(b) != "sk_test\n" {
		t.Fatalf("the key is kept in the team's secrets: %q", b)
	}
	if v := voice(); !v.Say || !v.Listen {
		t.Fatalf("a key that may do both: %+v", v)
	}

	say := func(text, token string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/voice/say", strings.NewReader(url.Values{"text": {text}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Rota-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	if resp, _ := say("Hello", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no token, no reading: %d", resp.StatusCode)
	}
	if resp, body := say("Merge them.", token); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/mpeg" || body != "mp3:Merge them." {
		t.Fatalf("read aloud: %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, body := say("", token); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, `"error"`) {
		t.Fatalf("nothing to read: %d %s", resp.StatusCode, body)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("audio", "speech.webm")
	part.Write([]byte("the words"))
	mw.Close()
	listen := func(token string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/api/voice/listen", bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-Rota-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := listen(""); resp.Body.Close() == nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no token, nothing written down: %d", resp.StatusCode)
	}
	resp = listen(token)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"text":"heard the words"`) {
		t.Fatalf("written down: %d %s", resp.StatusCode, b)
	}

	act(url.Values{"action": {"voice-key"}, "text": {""}})
	if v := voice(); v.Key || v.Say {
		t.Fatalf("the key removed: %+v", v)
	}
}

func TestTheLogNamesTheDevice(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/158.0.0.0 Safari/537.36":                   "Chrome on a Mac",
		"curl/8": "a computer",
	} {
		if got := device(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}
