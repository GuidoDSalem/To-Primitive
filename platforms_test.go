package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlatforms imitates Google's OAuth and YouTube upload, and TikTok's
// OAuth and Content Posting API, closely enough to drive the real code.
type fakePlatforms struct {
	t  *testing.T
	mu sync.Mutex

	challenge     string // TikTok PKCE challenge from the authorize URL
	ytUploads     int
	ytMeta        map[string]any
	ttInit        map[string]any
	ttBytes       int64
	ttFailInit    bool // next TikTok init answers an error
	refreshes     map[string]int
	expiresIn     int // seconds the access tokens last
	statusChecked int
}

func (f *fakePlatforms) handler() http.Handler {
	mux := http.NewServeMux()
	token := func(platform string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			f.mu.Lock()
			defer f.mu.Unlock()
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				if r.Form.Get("code") != "good-code" || r.Form.Get("client_secret") != "secret" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":"invalid_grant","error_description":"bad code"}`)
					return
				}
				if platform == "tiktok" {
					sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if hex.EncodeToString(sum[:]) != f.challenge || r.Form.Get("client_key") != "key" {
						w.WriteHeader(400)
						fmt.Fprint(w, `{"error":"invalid_request","error_description":"pkce"}`)
						return
					}
				}
			case "refresh_token":
				f.refreshes[platform]++
			}
			fmt.Fprintf(w, `{"access_token":"%s-access-%d","refresh_token":"%s-refresh","expires_in":%d,"refresh_expires_in":31536000}`,
				platform, f.refreshes[platform], platform, f.expiresIn)
		}
	}
	mux.HandleFunc("POST /token", token("youtube"))
	mux.HandleFunc("POST /v2/oauth/token/", token("tiktok"))
	auth := func(r *http.Request, platform string) bool {
		return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+platform+"-access-")
	}
	mux.HandleFunc("GET /youtube/v3/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"snippet":{"title":"Shape it up"}}]}`)
	})
	mux.HandleFunc("POST /upload/youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r, "youtube") || r.URL.Query().Get("uploadType") != "resumable" || r.Header.Get("X-Upload-Content-Length") == "" {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		json.NewDecoder(r.Body).Decode(&f.ytMeta)
		f.mu.Unlock()
		w.Header().Set("Location", "http://"+r.Host+"/upload-session/1")
	})
	mux.HandleFunc("PUT /upload-session/1", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		if n == 0 || r.Header.Get("Content-Type") != "video/mp4" {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.ytUploads++
		f.mu.Unlock()
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"yt123","status":{"privacyStatus":"public"}}`)
	})
	ok := func(w http.ResponseWriter, data string) {
		fmt.Fprintf(w, `{"data":%s,"error":{"code":"ok","message":""}}`, data)
	}
	mux.HandleFunc("POST /v2/post/publish/creator_info/query/", func(w http.ResponseWriter, r *http.Request) {
		// An unaudited app: only private.
		ok(w, `{"creator_username":"shapeitup","creator_nickname":"Shape","privacy_level_options":["SELF_ONLY"],"max_video_post_duration_sec":600}`)
	})
	mux.HandleFunc("POST /v2/post/publish/video/init/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.ttFailInit {
			f.ttFailInit = false
			fmt.Fprint(w, `{"data":{},"error":{"code":"spam_risk_too_many_posts","message":"try later"}}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&f.ttInit)
		ok(w, `{"publish_id":"v_pub_1","upload_url":"http://`+r.Host+`/tt-upload?token=x"}`)
	})
	mux.HandleFunc("PUT /tt-upload", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.ttBytes += n
		f.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Content-Range"), "bytes 0-") {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(201)
	})
	mux.HandleFunc("POST /v2/post/publish/status/fetch/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.statusChecked++
		n := f.statusChecked
		f.mu.Unlock()
		if n < 2 {
			ok(w, `{"status":"PROCESSING_UPLOAD"}`)
			return
		}
		ok(w, `{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["7300"]}`)
	})
	return mux
}

func TestPlatforms(t *testing.T) {
	dir, p, _ := pubFixture(t)
	f := &fakePlatforms{t: t, refreshes: map[string]int{}, expiresIn: 3600}
	fake := httptest.NewServer(f.handler())
	defer fake.Close()

	env := map[string]string{"YOUTUBE_CLIENT_ID": "yt-id", "YOUTUBE_CLIENT_SECRET": "secret", "TIKTOK_CLIENT_KEY": "key", "TIKTOK_CLIENT_SECRET": "secret"}
	acc := newAccounts(filepath.Join(t.TempDir(), ".cuentas.json"))
	acc.env = func(k string) string { return env[k] }
	acc.ep = platformEndpoints{GoogleAuth: fake.URL + "/auth", GoogleToken: fake.URL + "/token", YouTubeAPI: fake.URL,
		TikTokAuth: fake.URL + "/tiktok-auth", TikTokAPI: fake.URL}
	p.up = &platformUploader{acc: acc, poll: time.Millisecond, wait: 5 * time.Second}

	srv := newServer(filepath.Join(dir, "data"), filepath.Join(dir, "reels"))
	srv.pub, srv.acc = p, acc
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Connect both accounts as the browser would.
	connect := func(platform string) {
		res, err := noFollow.Get(ts.URL + "/api/publish/accounts/" + platform + "/connect")
		if err != nil || res.StatusCode != 302 {
			t.Fatalf("connect %s: %v %v", platform, err, res.Status)
		}
		u, _ := url.Parse(res.Header.Get("Location"))
		q := u.Query()
		wantRedirect := ts.URL + "/api/publish/oauth/" + platform + "/callback"
		if q.Get("redirect_uri") != wantRedirect {
			t.Errorf("%s redirect_uri %q", platform, q.Get("redirect_uri"))
		}
		if platform == "youtube" && (q.Get("access_type") != "offline" || !strings.Contains(q.Get("scope"), "youtube.upload")) {
			t.Errorf("google auth %v", q)
		}
		if platform == "tiktok" {
			if q.Get("client_key") != "key" || q.Get("code_challenge_method") != "S256" || !strings.Contains(q.Get("scope"), "video.publish") {
				t.Errorf("tiktok auth %v", q)
			}
			f.challenge = q.Get("code_challenge")
		}
		res, err = noFollow.Get(wantRedirect + "?code=good-code&state=" + q.Get("state"))
		if err != nil || res.StatusCode != 302 || !strings.Contains(res.Header.Get("Location"), "conectado="+platform) {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("callback %s: %v %s", platform, res.Status, b)
		}
	}
	connect("youtube")
	connect("tiktok")
	if res, _ := noFollow.Get(ts.URL + "/api/publish/oauth/youtube/callback?code=good-code&state=made-up"); res.StatusCode != 400 {
		t.Errorf("unknown state: %d", res.StatusCode)
	}
	status := acc.Status()
	if !status[0].Connected || status[0].Account != "Shape it up" || !status[1].Connected || status[1].Account != "@shapeitup" {
		t.Fatalf("status %+v", status)
	}
	if fi, err := os.Stat(acc.path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("tokens file: %v %v", err, fi.Mode())
	}

	// Publish on both. TikTok fails the first time: YouTube isn't repeated.
	s := p.state.Settings
	s.Platforms = []string{"youtube", "tiktok"}
	if err := p.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	writeSidecar(filepath.Join(dir, "b viejo.mp4"), map[string]any{"title": "La <Mona> Lisa", "description": "Con formas #Shorts", "tags": []string{"arte", "low poly"}})
	f.ttFailInit = true
	if _, err := p.Publish(context.Background(), "b viejo.mp4", "a mano"); err == nil || !strings.Contains(err.Error(), "spam_risk_too_many_posts") || !strings.Contains(err.Error(), "no se repite") {
		t.Fatalf("first try: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b viejo.mp4")); err != nil {
		t.Fatal("left the queue after failing")
	}
	q := p.Overview().Queue
	if q[0].Name != "b viejo.mp4" || q[0].Uploads["youtube"].ID != "yt123" {
		t.Fatalf("partial upload not noted: %+v", q[0].Uploads)
	}
	// Access tokens expire: the next publish refreshes them.
	acc.mu.Lock()
	for _, tok := range acc.tokens {
		tok.Expiry = time.Now().Add(-time.Minute)
	}
	acc.mu.Unlock()
	name, err := p.Publish(context.Background(), "b viejo.mp4", "a mano")
	if err != nil {
		t.Fatalf("second try: %v", err)
	}
	if f.ytUploads != 1 {
		t.Errorf("YouTube got %d uploads", f.ytUploads)
	}
	if f.refreshes["tiktok"] != 1 {
		t.Errorf("tiktok refreshes %v", f.refreshes)
	}
	pub := p.Overview().Published
	if len(pub) != 1 || pub[0].Name != name || !strings.Contains(pub[0].Result, "https://youtube.com/shorts/yt123") ||
		!strings.Contains(pub[0].Result, "https://www.tiktok.com/@shapeitup/video/7300") || !strings.Contains(pub[0].Result, "privado") {
		t.Fatalf("published %+v", pub)
	}
	// What each platform got.
	snip := f.ytMeta["snippet"].(map[string]any)
	if snip["title"] != "La Mona Lisa" || f.ytMeta["status"].(map[string]any)["privacyStatus"] != "public" {
		t.Errorf("youtube meta %v", f.ytMeta)
	}
	post := f.ttInit["post_info"].(map[string]any)
	src := f.ttInit["source_info"].(map[string]any)
	fi, _ := os.Stat(filepath.Join(dir, publishedDir, name))
	if post["privacy_level"] != "SELF_ONLY" || !strings.Contains(post["title"].(string), "#arte") || !strings.Contains(post["title"].(string), "#lowpoly") ||
		src["total_chunk_count"] != 1.0 || int64(src["video_size"].(float64)) != fi.Size() || f.ttBytes != fi.Size() {
		t.Errorf("tiktok init %v, %d bytes sent", f.ttInit, f.ttBytes)
	}

	// Without platforms: simulated, as before.
	s.Platforms = nil
	p.SetSettings(s)
	if _, err := p.Publish(context.Background(), "", "a mano"); err != nil {
		t.Fatal(err)
	}
	if pub := p.Overview().Published; !strings.Contains(pub[0].Result, "simulado") {
		t.Errorf("simulated: %q", pub[0].Result)
	}
	if err := p.SetSettings(PublishSettings{Times: []string{"10:00"}, Days: []int{1}, TZ: "UTC", Platforms: []string{"myspace"}}); err == nil {
		t.Error("unknown platform accepted")
	}
}

func TestTikTokChunks(t *testing.T) {
	for _, c := range []struct {
		size        int64
		chunk       int64
		count, last int64
	}{
		{3 << 20, 3 << 20, 1, 3 << 20},
		{64 << 20, 64 << 20, 1, 64 << 20},
		{50_000_123 * 2, 10 << 20, 9, 100_000_246 - 8*(10<<20)},
	} {
		chunk, count := tiktokChunks(c.size)
		last := c.size - int64(count-1)*chunk
		if chunk != c.chunk || int64(count) != c.count || last != c.last || last > 128<<20 {
			t.Errorf("tiktokChunks(%d) = %d × %d (last %d)", c.size, chunk, count, last)
		}
	}
}
