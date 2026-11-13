package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Web push: the human's phone is told when an agent asks a question. The pages register the
// phone's browser (its push subscription) in push.json; when a question is asked, each
// subscription gets a push without content, signed with the team's VAPID key, and the phone's
// service worker fetches the open questions from the pages, over the tailnet, to show them. No
// word of a question goes through the push service.

const (
	pushFile  = "push.json"                // the subscriptions, in the team folder
	vapidFile = "secrets/web-push-key.pem" // the team's VAPID key, made once
)

// pushClient sends the pushes, never following a redirect; tests replace it.
var pushClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// pushServices are the push services a subscription may name: the browsers' own. Any other
// address would make rota post wherever a page told it to.
var pushServices = []string{"fcm.googleapis.com", "push.services.mozilla.com", "push.apple.com", "notify.windows.com"}

func pushService(host string) bool {
	for _, s := range pushServices {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// PushSubscription is one device the human turned notifications on for.
type PushSubscription struct {
	Endpoint string    `json:"endpoint"`
	Device   string    `json:"device,omitempty"` // the browser, as it said
	Added    time.Time `json:"added"`
}

var pushMu sync.Mutex // one change of push.json at a time in this process

// vapidKey is the team's VAPID key, made the first time it is needed.
func (c *Config) vapidKey() (*ecdsa.PrivateKey, error) {
	path := c.Path(vapidFile)
	read := func() (*ecdsa.PrivateKey, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if blk, _ := pem.Decode(b); blk != nil {
			return x509.ParseECPrivateKey(blk.Bytes)
		}
		return nil, fmt.Errorf("%s is not a key", vapidFile)
	}
	if key, err := read(); !os.IsNotExist(err) {
		return key, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".web-push-key-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	tmp.Write(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	tmp.Close()
	if err := os.Link(tmp.Name(), path); os.IsExist(err) { // made meanwhile by another rota: that one is the key
		return read()
	} else if err != nil {
		return nil, err
	}
	return key, nil
}

// VAPIDPublicKey is the team's VAPID public key as browsers take it: the uncompressed point,
// base64url.
func (c *Config) VAPIDPublicKey() (string, error) {
	key, err := c.vapidKey()
	if err != nil {
		return "", err
	}
	return b64(elliptic.Marshal(elliptic.P256(), key.X, key.Y)), nil //nolint:staticcheck // the uncompressed point is what browsers want
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// PushSubscriptions are the devices that get a push when a question is asked.
func (c *Config) PushSubscriptions() []PushSubscription {
	subs, _ := c.readPush()
	return subs
}

func (c *Config) readPush() ([]PushSubscription, error) {
	var subs []PushSubscription
	b, err := os.ReadFile(c.Path(pushFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &subs)
	}
	return subs, err
}

// changePush rewrites push.json with what change makes of it, under a lock every rota on the
// machine takes: the pages and the organizer may both change it. An unreadable file is left
// as it is.
func (c *Config) changePush(change func([]PushSubscription) []PushSubscription) error {
	pushMu.Lock()
	defer pushMu.Unlock()
	lock, err := os.OpenFile(c.Path(pushFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	subs, err := c.readPush()
	if err != nil {
		return fmt.Errorf("%s: %w; not changed", pushFile, err)
	}
	b, _ := json.MarshalIndent(change(subs), "", "  ")
	tmp, err := os.CreateTemp(c.Root, "."+pushFile+"-*")
	if err != nil {
		return err
	}
	tmp.Write(b)
	tmp.Close()
	os.Chmod(tmp.Name(), 0o600)
	return os.Rename(tmp.Name(), c.Path(pushFile))
}

// Subscribe registers a device's push endpoint, at a browser's push service; one already
// registered is kept once.
func (c *Config) Subscribe(endpoint, device string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || !pushService(u.Hostname()) {
		return fmt.Errorf("a push endpoint is an https address at a browser's push service")
	}
	return c.changePush(func(subs []PushSubscription) []PushSubscription {
		for _, s := range subs {
			if s.Endpoint == endpoint {
				return subs
			}
		}
		return append(subs, PushSubscription{Endpoint: endpoint, Device: device, Added: time.Now()})
	})
}

// Unsubscribe forgets a device's push endpoint.
func (c *Config) Unsubscribe(endpoint string) error {
	return c.changePush(func(subs []PushSubscription) []PushSubscription {
		var keep []PushSubscription
		for _, s := range subs {
			if s.Endpoint != endpoint {
				keep = append(keep, s)
			}
		}
		return keep
	})
}

// PushAll tells every registered device that a question waits. A device its push service no
// longer knows (404, 410) is forgotten. It says how many devices it reached.
func (c *Config) PushAll(ctx context.Context) (int, error) {
	key, err := c.vapidKey()
	if err != nil {
		return 0, err
	}
	subs := c.PushSubscriptions()
	codes, errs := make([]int, len(subs)), make([]error, len(subs))
	var wg sync.WaitGroup
	for i, s := range subs { // all at once: a slow push service holds up no other
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], errs[i] = sendPush(ctx, key, s.Endpoint) }()
	}
	wg.Wait()
	reached, failed := 0, []string{}
	for i, s := range subs {
		switch code, err := codes[i], errs[i]; {
		case err != nil:
			failed = append(failed, err.Error())
		case code == http.StatusNotFound || code == http.StatusGone:
			c.Unsubscribe(s.Endpoint)
		case code >= 300:
			failed = append(failed, fmt.Sprintf("%s: HTTP %d", hostOf(s.Endpoint), code))
		default:
			reached++
		}
	}
	if len(failed) > 0 {
		return reached, fmt.Errorf("push: %s", strings.Join(failed, "; "))
	}
	return reached, nil
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return "?"
}

// sendPush sends one push without content: the device only learns that something waits.
func sendPush(ctx context.Context, key *ecdsa.PrivateKey, endpoint string) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	jwt, err := vapidJWT(key, u.Scheme+"://"+u.Host, time.Now())
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+b64(elliptic.Marshal(elliptic.P256(), key.X, key.Y))) //nolint:staticcheck
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")
	resp, err := pushClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %v", u.Host, err)
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// vapidJWT is the signed claim a push service checks (RFC 8292): for audience aud, valid
// twelve hours.
func vapidJWT(key *ecdsa.PrivateKey, aud string, now time.Time) (string, error) {
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": now.Add(12 * time.Hour).Unix(), "sub": "https://github.com/marashiai/rota"})
	signing := b64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64(sig), nil
}

// verifyJWT checks a VAPID token against the public key: for tests.
func verifyJWT(token string, pub *ecdsa.PublicKey) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}
