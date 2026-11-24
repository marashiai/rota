package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The pages read aloud and take dictation. They do it with ElevenLabs when the human gave rota an
// ElevenLabs key that may (a key can be limited to some of its uses), and with the browser's own
// voice and dictation otherwise. The key stays on this machine: the pages send rota the text to
// read or what was said, and rota asks ElevenLabs.

const voiceKeyFile = "secrets/elevenlabs.key"

// The ElevenLabs models rota uses: the fast one that reads in many languages, and the one that
// writes down what was said, in whatever language.
const (
	sayModel    = "eleven_flash_v2_5"
	listenModel = "scribe_v2"
	// SayMax is the most one reading asks ElevenLabs to read: the pages send a longer text in
	// parts, so the first part plays sooner.
	SayMax = 5000
	// ListenMax is the largest recording the pages send to be written down.
	ListenMax = 25 << 20
)

// ElevenAPI is where ElevenLabs answers; a test points it at its own.
var ElevenAPI = "https://api.elevenlabs.io"

// elevenClient follows no redirect: one would carry the key to wherever it points.
var elevenClient = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// VoiceKey is the ElevenLabs key the human gave rota; "" for none.
func (c *Config) VoiceKey() string {
	b, _ := os.ReadFile(c.Path(voiceKeyFile))
	return strings.TrimSpace(string(b))
}

// SetVoiceKey keeps key as the ElevenLabs key, readable by this user only; "" removes it.
func (c *Config) SetVoiceKey(key string) error {
	key = strings.TrimSpace(key)
	path := c.Path(voiceKeyFile)
	if key == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if strings.ContainsAny(key, " \t\r\n") || len(key) > 200 {
		return fmt.Errorf("that is not an ElevenLabs key: a key is one word")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".elevenlabs.key.*.tmp") // CreateTemp makes it 0600
	if err != nil {
		return err
	}
	_, err = f.WriteString(key + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// VoiceUse says who reads aloud and who takes dictation: ElevenLabs, or the browser, and why not
// ElevenLabs.
type VoiceUse struct {
	Key       bool   `json:"key"`    // the human gave rota a key
	Say       bool   `json:"say"`    // ElevenLabs reads aloud
	Listen    bool   `json:"listen"` // ElevenLabs writes down what was said
	SayWhy    string `json:"sayWhy,omitempty"`
	ListenWhy string `json:"listenWhy,omitempty"`
}

// voiceUse is what rota found the key may do, for one key and voice, until again.
type voiceUse struct {
	of                string // a hash of the key and the voice
	say, listen       bool
	sayWhy, listenWhy string
	sayAgain          time.Time // when to ask ElevenLabs again
	listenAgain       time.Time
}

var (
	voiceMu    sync.Mutex // one check at a time; pages that ask meanwhile wait for its answer
	voiceFound voiceUse
)

// How long what rota found holds: a key that worked is asked about again the next day, one that
// ElevenLabs refused an hour later (its permissions may change), and one that could not be asked
// (no network, ElevenLabs down or busy) five minutes later.
const (
	voiceWorked  = 24 * time.Hour
	voiceRefused = time.Hour
	voiceFailed  = 5 * time.Minute
)

// Voice says who reads aloud and who takes dictation. With a key, it asks ElevenLabs, at most
// as often as what it found holds, whether the key may: it reads one word and writes down half a
// second of silence, which costs next to nothing.
func (c *Config) Voice(ctx context.Context) VoiceUse {
	key := c.VoiceKey()
	if key == "" {
		return VoiceUse{SayWhy: "no ElevenLabs key", ListenWhy: "no ElevenLabs key"}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second) // a page closed meanwhile does not cut it short
	defer cancel()
	voiceMu.Lock()
	defer voiceMu.Unlock()
	of := voiceOf(key, c.voice())
	if voiceFound.of != of {
		voiceFound = voiceUse{of: of}
	}
	now := time.Now()
	if !now.Before(voiceFound.sayAgain) {
		err := elevenSay(ctx, key, c.voice(), "Hi", io.Discard)
		voiceFound.say, voiceFound.sayWhy, voiceFound.sayAgain = err == nil, whyNot(err), now.Add(again(err))
	}
	if !now.Before(voiceFound.listenAgain) {
		_, err := elevenListen(ctx, key, bytes.NewReader(silence()), "silence.wav", "audio/wav")
		voiceFound.listen, voiceFound.listenWhy, voiceFound.listenAgain = err == nil, whyNot(err), now.Add(again(err))
	}
	return VoiceUse{Key: true, Say: voiceFound.say, Listen: voiceFound.listen, SayWhy: voiceFound.sayWhy, ListenWhy: voiceFound.listenWhy}
}

// voice is the ElevenLabs voice that reads aloud.
func (c *Config) voice() string { return or(strings.TrimSpace(c.VoiceID), "JBFqnCBsd6RMkjVDRZzb") }

func voiceOf(key, voice string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + voice))
	return string(sum[:])
}

func again(err error) time.Duration {
	switch {
	case err == nil:
		return voiceWorked
	case lasting(err):
		return voiceRefused
	}
	return voiceFailed
}

// lasting says ElevenLabs refused the key itself, or what it asked for: the key is wrong, may not
// do this, or has no credits left, or the voice is not one it has. Anything else (too many at
// once, ElevenLabs down, a recording it cannot read) may pass on the next try.
func lasting(err error) bool {
	var e *ElevenError
	return errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || e.Code == "voice_not_found")
}

func whyNot(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// noteRefused notes that ElevenLabs refused the key one of its uses (say or listen) while the pages
// used it, so the next pages use the browser's own until rota asks again.
func noteRefused(key, voice string, say bool, err error) {
	if !lasting(err) {
		return // the next one may work
	}
	voiceMu.Lock()
	defer voiceMu.Unlock()
	if voiceFound.of != voiceOf(key, voice) {
		return
	}
	if say {
		voiceFound.say, voiceFound.sayWhy, voiceFound.sayAgain = false, err.Error(), time.Now().Add(voiceRefused)
	} else {
		voiceFound.listen, voiceFound.listenWhy, voiceFound.listenAgain = false, err.Error(), time.Now().Add(voiceRefused)
	}
}

// ReadAloud reads text aloud with ElevenLabs, writing the MP3 to w; it writes nothing when it fails.
func (c *Config) ReadAloud(ctx context.Context, text string, w io.Writer) error {
	key, voice := c.VoiceKey(), c.voice()
	if key == "" {
		return fmt.Errorf("no ElevenLabs key")
	}
	if text = strings.TrimSpace(text); text == "" || len([]rune(text)) > SayMax {
		return fmt.Errorf("a text to read is 1 to %d characters", SayMax)
	}
	err := elevenSay(ctx, key, voice, text, w)
	noteRefused(key, voice, true, err)
	return err
}

// Transcribe writes down what a recording says, with ElevenLabs.
func (c *Config) Transcribe(ctx context.Context, audio io.Reader, name, contentType string) (string, error) {
	key := c.VoiceKey()
	if key == "" {
		return "", fmt.Errorf("no ElevenLabs key")
	}
	text, err := elevenListen(ctx, key, audio, name, contentType)
	noteRefused(key, c.voice(), false, err)
	return text, err
}

// ElevenError is ElevenLabs refusing a request, as it says why.
type ElevenError struct {
	Status  int
	Code    string // its own word for it, such as missing_permissions or invalid_api_key
	Message string
}

func (e *ElevenError) Error() string {
	if e.Message != "" {
		return "ElevenLabs: " + strings.TrimSuffix(e.Message, ".")
	}
	if e.Code != "" {
		return "ElevenLabs: " + strings.ReplaceAll(e.Code, "_", " ")
	}
	return fmt.Sprintf("ElevenLabs answered %d", e.Status)
}

// elevenError reads why ElevenLabs refused: {"detail": {"status": ..., "message": ...}}, or a
// detail that is only words.
func elevenError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &ElevenError{Status: resp.StatusCode}
	var body struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(b, &body) == nil && len(body.Detail) > 0 {
		var d struct{ Status, Message string }
		if json.Unmarshal(body.Detail, &d) == nil {
			e.Code, e.Message = d.Status, d.Message
		} else {
			var s string
			if json.Unmarshal(body.Detail, &s) == nil {
				e.Message = s
			}
		}
	}
	return e
}

func elevenSay(ctx context.Context, key, voice, text string, w io.Writer) error {
	body, _ := json.Marshal(map[string]string{"text": text, "model_id": sayModel})
	u := ElevenAPI + "/v1/text-to-speech/" + url.PathEscape(voice) + "/stream?output_format=mp3_44100_128"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	resp, err := elevenClient.Do(req)
	if err != nil {
		return fmt.Errorf("ElevenLabs did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return elevenError(resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func elevenListen(ctx context.Context, key string, audio io.Reader, name, contentType string) (string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := mw.WriteField("model_id", listenModel)
		if err == nil {
			err = mw.WriteField("tag_audio_events", "false") // the words, not (laughs)
		}
		if err == nil {
			h := make(map[string][]string)
			h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename=%q`, or(name, "speech"))}
			h["Content-Type"] = []string{or(contentType, "application/octet-stream")}
			var part io.Writer
			if part, err = mw.CreatePart(h); err == nil {
				_, err = io.Copy(part, audio)
			}
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ElevenAPI+"/v1/speech-to-text", pr)
	if err != nil {
		pr.Close()
		return "", err
	}
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := elevenClient.Do(req)
	if err != nil {
		pr.CloseWithError(err)
		return "", fmt.Errorf("ElevenLabs did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", elevenError(resp)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("ElevenLabs answered what rota cannot read: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// silence is half a second of silence as a WAV file: 16 kHz, mono, 16 bits.
func silence() []byte {
	const rate, samples = 16000, 8000
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+samples*2))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, struct {
		Size              uint32
		Format, Channels  uint16
		Rate, BytesPerSec uint32
		BlockAlign, Bits  uint16
	}{16, 1, 1, rate, rate * 2, 2, 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(samples*2))
	b.Write(make([]byte, samples*2))
	return b.Bytes()
}
