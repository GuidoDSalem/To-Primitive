package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Publishing for real: YouTube (Data API v3, resumable upload) and TikTok
// (Content Posting API, Direct Post). Each account is connected once with
// OAuth from the Publicar page; its tokens live in the library folder
// (.cuentas.json), not in the publishing folder. The app's own credentials
// come from the environment:
//
//	YOUTUBE_CLIENT_ID, YOUTUBE_CLIENT_SECRET  (Google Cloud, OAuth client "Web application")
//	TIKTOK_CLIENT_KEY, TIKTOK_CLIENT_SECRET   (TikTok for Developers, Login Kit + Content Posting API)
//
// The redirect URI to register is http://<host>/api/publish/oauth/<platform>/callback,
// as the page shows it.

var platformIDs = []string{"youtube", "tiktok"}

var platformNames = map[string]string{"youtube": "YouTube", "tiktok": "TikTok"}

// platformEndpoints are the providers' URLs (other ones in tests).
type platformEndpoints struct {
	GoogleAuth, GoogleToken, YouTubeAPI string
	TikTokAuth, TikTokAPI               string
}

var realEndpoints = platformEndpoints{
	GoogleAuth:  "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleToken: "https://oauth2.googleapis.com/token",
	YouTubeAPI:  "https://www.googleapis.com",
	TikTokAuth:  "https://www.tiktok.com/v2/auth/authorize/",
	TikTokAPI:   "https://open.tiktokapis.com",
}

type oauthToken struct {
	Access        string    `json:"access"`
	Refresh       string    `json:"refresh"`
	Expiry        time.Time `json:"expiry"`
	RefreshExpiry time.Time `json:"refreshExpiry,omitzero"`
	Account       string    `json:"account"` // channel or nickname, to show it
	Connected     time.Time `json:"connected"`
}

type pendingAuth struct {
	platform, verifier, redirect string
	expires                      time.Time
}

// accounts holds the connected accounts and does the OAuth dance.
type accounts struct {
	path string
	ep   platformEndpoints
	http *http.Client
	env  func(string) string

	mu      sync.Mutex
	tokens  map[string]*oauthToken
	pending map[string]pendingAuth // by state
}

func newAccounts(path string) *accounts {
	a := &accounts{path: path, ep: realEndpoints, http: &http.Client{Timeout: 30 * time.Minute}, env: os.Getenv,
		tokens: map[string]*oauthToken{}, pending: map[string]pendingAuth{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &a.tokens); err != nil {
			log.Printf("accounts: ignoring unreadable %s: %v", path, err)
		}
	}
	return a
}

func (a *accounts) saveLocked() error {
	b, _ := json.MarshalIndent(a.tokens, "", "  ")
	return os.WriteFile(a.path, b, 0o600)
}

// client is the app's credentials for a platform ("" if not configured).
func (a *accounts) client(platform string) (id, secret string) {
	switch platform {
	case "youtube":
		return a.env("YOUTUBE_CLIENT_ID"), a.env("YOUTUBE_CLIENT_SECRET")
	case "tiktok":
		return a.env("TIKTOK_CLIENT_KEY"), a.env("TIKTOK_CLIENT_SECRET")
	}
	return "", ""
}

// AccountStatus is how a platform shows on the page.
type AccountStatus struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Configured bool      `json:"configured"` // the app's credentials are in the environment
	Missing    string    `json:"missing,omitempty"`
	Connected  bool      `json:"connected"`
	Account    string    `json:"account,omitempty"`
	Since      time.Time `json:"since,omitzero"`
}

func (a *accounts) Status() []AccountStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AccountStatus
	for _, id := range platformIDs {
		s := AccountStatus{ID: id, Name: platformNames[id]}
		cid, secret := a.client(id)
		s.Configured = cid != "" && secret != ""
		if !s.Configured {
			s.Missing = map[string]string{"youtube": "YOUTUBE_CLIENT_ID y YOUTUBE_CLIENT_SECRET", "tiktok": "TIKTOK_CLIENT_KEY y TIKTOK_CLIENT_SECRET"}[id]
		}
		if t := a.tokens[id]; t != nil {
			s.Connected, s.Account, s.Since = true, t.Account, t.Connected
		}
		out = append(out, s)
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// AuthURL is where the browser goes to connect an account; redirect is
// this server's callback, as registered with the platform.
func (a *accounts) AuthURL(platform, redirect string) (string, error) {
	id, _ := a.client(platform)
	if id == "" {
		return "", fmt.Errorf("faltan las credenciales de %s en el .env", platformNames[platform])
	}
	state := randomHex(16)
	p := pendingAuth{platform: platform, redirect: redirect, expires: time.Now().Add(15 * time.Minute)}
	q := url.Values{"state": {state}, "redirect_uri": {redirect}, "response_type": {"code"}}
	var base string
	switch platform {
	case "youtube":
		base = a.ep.GoogleAuth
		q.Set("client_id", id)
		q.Set("scope", "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly")
		q.Set("access_type", "offline") // a refresh token, to publish while nobody is looking
		q.Set("prompt", "consent")      // and a new one every time it's connected
	case "tiktok":
		base = a.ep.TikTokAuth
		q.Set("client_key", id)
		q.Set("scope", "user.info.basic,video.publish")
		// Back straight to this computer (a "Desktop" app): PKCE, as TikTok
		// wants it there (hex SHA-256). Through an https page (a "Web" app,
		// TIKTOK_REDIRECT_URI) TikTok doesn't use it.
		if strings.HasPrefix(redirect, "http://") {
			p.verifier = randomHex(32)
			sum := sha256.Sum256([]byte(p.verifier))
			q.Set("code_challenge", hex.EncodeToString(sum[:]))
			q.Set("code_challenge_method", "S256")
		}
	default:
		return "", errNotFound
	}
	a.mu.Lock()
	for s, old := range a.pending {
		if time.Now().After(old.expires) {
			delete(a.pending, s)
		}
	}
	a.pending[state] = p
	a.mu.Unlock()
	return base + "?" + q.Encode(), nil
}

type tokenAnswer struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// exchange asks the platform's token endpoint for tokens.
func (a *accounts) exchange(ctx context.Context, platform string, form url.Values) (*tokenAnswer, error) {
	id, secret := a.client(platform)
	endpoint := a.ep.GoogleToken
	if platform == "tiktok" {
		endpoint = a.ep.TikTokAPI + "/v2/oauth/token/"
		form.Set("client_key", id)
	} else {
		form.Set("client_id", id)
	}
	form.Set("client_secret", secret)
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var t tokenAnswer
	body, _ := io.ReadAll(res.Body)
	json.Unmarshal(body, &t)
	if res.StatusCode != 200 || t.AccessToken == "" {
		msg := t.ErrorDescription
		if msg == "" {
			msg = t.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("%s no dio el acceso (%d): %s", platformNames[platform], res.StatusCode, msg)
	}
	return &t, nil
}

func (t *tokenAnswer) apply(tok *oauthToken, now time.Time) {
	tok.Access = t.AccessToken
	tok.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	if t.RefreshToken != "" { // TikTok may rotate it; Google sends it only on consent
		tok.Refresh = t.RefreshToken
	}
	if t.RefreshExpiresIn > 0 {
		tok.RefreshExpiry = now.Add(time.Duration(t.RefreshExpiresIn) * time.Second)
	}
}

// Callback finishes connecting an account with the code the platform sent.
func (a *accounts) Callback(ctx context.Context, platform, state, code string) error {
	a.mu.Lock()
	p, ok := a.pending[state]
	delete(a.pending, state)
	a.mu.Unlock()
	if !ok || p.platform != platform || time.Now().After(p.expires) {
		return errors.New("el pedido de conexión venció o no es de esta página: probá conectar de nuevo")
	}
	form := url.Values{"code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {p.redirect}}
	if p.verifier != "" {
		form.Set("code_verifier", p.verifier)
	}
	t, err := a.exchange(ctx, platform, form)
	if err != nil {
		return err
	}
	if t.RefreshToken == "" {
		return fmt.Errorf("%s no dio un permiso duradero (refresh token): quitá el acceso de la app en tu cuenta y conectá de nuevo", platformNames[platform])
	}
	tok := &oauthToken{Connected: time.Now()}
	t.apply(tok, time.Now())
	tok.Account = a.accountName(ctx, platform, tok.Access)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens[platform] = tok
	return a.saveLocked()
}

// accountName is the channel or the nickname ("" if it can't be read).
func (a *accounts) accountName(ctx context.Context, platform, access string) string {
	switch platform {
	case "youtube":
		var r struct {
			Items []struct {
				Snippet struct{ Title string } `json:"snippet"`
			} `json:"items"`
		}
		if a.call(ctx, "GET", a.ep.YouTubeAPI+"/youtube/v3/channels?part=snippet&mine=true", access, nil, &r) == nil && len(r.Items) > 0 {
			return r.Items[0].Snippet.Title
		}
	case "tiktok":
		if info, err := a.creatorInfo(ctx, access); err == nil {
			return "@" + info.Username
		}
	}
	return ""
}

func (a *accounts) Disconnect(platform string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tokens, platform)
	return a.saveLocked()
}

// access is a valid access token for a platform, refreshed if needed.
func (a *accounts) access(ctx context.Context, platform string) (string, error) {
	a.mu.Lock()
	tok := a.tokens[platform]
	if tok == nil {
		a.mu.Unlock()
		return "", fmt.Errorf("%s no está conectado", platformNames[platform])
	}
	if time.Until(tok.Expiry) > 2*time.Minute {
		defer a.mu.Unlock()
		return tok.Access, nil
	}
	refresh := tok.Refresh
	a.mu.Unlock()
	t, err := a.exchange(ctx, platform, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err != nil {
		return "", fmt.Errorf("%w (si sigue, desconectá y conectá de nuevo la cuenta)", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t.apply(tok, time.Now())
	a.saveLocked()
	return tok.Access, nil
}

// call sends a JSON request with a bearer token and decodes the answer.
func (a *accounts) call(ctx context.Context, method, endpoint, access string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return apiError(res.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// apiError keeps the useful part of an API's error answer.
func apiError(status int, body []byte) error {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && len(e.Error) > 0 {
		var g struct{ Message, Code string }
		if json.Unmarshal(e.Error, &g) == nil && (g.Message != "" || g.Code != "") {
			msg = strings.TrimSpace(g.Code + " " + g.Message)
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Errorf("error %d: %s", status, msg)
}

// ---------- uploading ----------

// platformUploader publishes on the platforms chosen in the settings. What
// each one returned is noted in the video's JSON ("uploads"), so a retry
// after a failure doesn't publish twice where it already worked.
type platformUploader struct {
	acc  *accounts
	poll time.Duration // between TikTok status checks
	wait time.Duration // how long to wait for TikTok to finish
}

type uploadResult struct {
	ID  string    `json:"id"`
	URL string    `json:"url,omitempty"`
	At  time.Time `json:"at"`
	Msg string    `json:"msg,omitempty"`
}

func (u *platformUploader) Upload(ctx context.Context, path string, item PubItem, s PublishSettings) (string, error) {
	if len(s.Platforms) == 0 {
		return simulatedUploader{}.Upload(ctx, path, item, s)
	}
	side := readSidecar(path)
	done := map[string]uploadResult{}
	if u, ok := side["uploads"]; ok {
		b, _ := json.Marshal(u)
		json.Unmarshal(b, &done)
		if done == nil {
			done = map[string]uploadResult{}
		}
	}
	var parts, failed []string
	for _, p := range s.Platforms {
		if r, ok := done[p]; ok {
			parts = append(parts, describeUpload(p, r))
			continue
		}
		var r uploadResult
		var err error
		switch p {
		case "youtube":
			r, err = u.youtube(ctx, path, item, s)
		case "tiktok":
			r, err = u.tiktok(ctx, path, item, s)
		default:
			continue
		}
		if err != nil {
			failed = append(failed, platformNames[p]+": "+err.Error())
			continue
		}
		r.At = time.Now()
		done[p] = r
		side["uploads"] = done
		writeSidecar(path, side) // right away: a later failure must not lose it
		parts = append(parts, describeUpload(p, r))
	}
	if len(failed) > 0 {
		msg := strings.Join(failed, " · ")
		if len(parts) > 0 {
			msg += " (ya quedó en " + strings.Join(parts, " · ") + "; al reintentar no se repite)"
		}
		return "", errors.New(msg)
	}
	return strings.Join(parts, " · "), nil
}

func describeUpload(p string, r uploadResult) string {
	s := platformNames[p]
	if r.URL != "" {
		s += " " + r.URL
	}
	if r.Msg != "" {
		s += " (" + r.Msg + ")"
	}
	return s
}

func cut(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// youtube uploads with the resumable protocol (one session, the whole file).
func (u *platformUploader) youtube(ctx context.Context, path string, item PubItem, s PublishSettings) (uploadResult, error) {
	access, err := u.acc.access(ctx, "youtube")
	if err != nil {
		return uploadResult{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return uploadResult{}, err
	}
	defer f.Close()
	fi, _ := f.Stat()
	title := strings.NewReplacer("<", "", ">", "").Replace(item.Title) // not allowed in titles
	var tags []string
	n := 0
	for _, t := range item.Tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		if t == "" || n+len(t) > 450 { // YouTube: 500 characters in all
			continue
		}
		tags = append(tags, t)
		n += len(t) + 1
	}
	privacy := s.YouTubePrivacy
	if privacy == "" {
		privacy = "public"
	}
	meta := map[string]any{
		"snippet": map[string]any{"title": cut(title, 100), "description": cut(item.Description, 4900), "tags": tags, "categoryId": "22"},
		"status":  map[string]any{"privacyStatus": privacy, "selfDeclaredMadeForKids": false},
	}
	b, _ := json.Marshal(meta)
	req, _ := http.NewRequestWithContext(ctx, "POST", u.acc.ep.YouTubeAPI+"/upload/youtube/v3/videos?uploadType=resumable&part="+url.QueryEscape("snippet,status"), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", fmt.Sprint(fi.Size()))
	req.Header.Set("X-Upload-Content-Type", "video/mp4")
	res, err := u.acc.http.Do(req)
	if err != nil {
		return uploadResult{}, err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	session := res.Header.Get("Location")
	if res.StatusCode != 200 || session == "" {
		return uploadResult{}, apiError(res.StatusCode, body)
	}
	put, _ := http.NewRequestWithContext(ctx, "PUT", session, f)
	put.ContentLength = fi.Size()
	put.Header.Set("Authorization", "Bearer "+access)
	put.Header.Set("Content-Type", "video/mp4")
	res, err = u.acc.http.Do(put)
	if err != nil {
		return uploadResult{}, err
	}
	defer res.Body.Close()
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return uploadResult{}, apiError(res.StatusCode, body)
	}
	var v struct {
		ID     string `json:"id"`
		Status struct {
			PrivacyStatus string `json:"privacyStatus"`
		} `json:"status"`
	}
	json.Unmarshal(body, &v)
	if v.ID == "" {
		return uploadResult{}, errors.New("YouTube no devolvió el id del video")
	}
	r := uploadResult{ID: v.ID, URL: "https://youtube.com/shorts/" + v.ID}
	if v.Status.PrivacyStatus != "" && v.Status.PrivacyStatus != "public" {
		r.Msg = v.Status.PrivacyStatus
	}
	return r, nil
}

// ---------- TikTok ----------

type tiktokAnswer struct {
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// tiktokCall is a Content Posting API call (errors come in error.code).
func (a *accounts) tiktokCall(ctx context.Context, path, access string, body, out any) error {
	var ans tiktokAnswer
	err := a.call(ctx, "POST", a.ep.TikTokAPI+path, access, body, &ans)
	if err != nil {
		return err
	}
	if ans.Error.Code != "" && ans.Error.Code != "ok" {
		return fmt.Errorf("%s: %s", ans.Error.Code, ans.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(ans.Data, out)
	}
	return nil
}

type creatorInfo struct {
	Username       string   `json:"creator_username"`
	Nickname       string   `json:"creator_nickname"`
	PrivacyOptions []string `json:"privacy_level_options"`
	MaxDuration    int      `json:"max_video_post_duration_sec"`
}

func (a *accounts) creatorInfo(ctx context.Context, access string) (creatorInfo, error) {
	var info creatorInfo
	err := a.tiktokCall(ctx, "/v2/post/publish/creator_info/query/", access, map[string]any{}, &info)
	return info, err
}

// tiktokChunks splits a file as TikTok asks: one piece up to 64 MB;
// otherwise 10 MB pieces, the last one with the rest.
func tiktokChunks(size int64) (chunk int64, count int) {
	const max, piece = 64 << 20, 10 << 20
	if size <= max {
		return size, 1
	}
	return piece, int(size / piece)
}

func (u *platformUploader) tiktok(ctx context.Context, path string, item PubItem, s PublishSettings) (uploadResult, error) {
	access, err := u.acc.access(ctx, "tiktok")
	if err != nil {
		return uploadResult{}, err
	}
	info, err := u.acc.creatorInfo(ctx, access)
	if err != nil {
		return uploadResult{}, err
	}
	if info.MaxDuration > 0 && item.Duration > float64(info.MaxDuration) {
		return uploadResult{}, fmt.Errorf("dura %.0f s y la cuenta permite hasta %d s", item.Duration, info.MaxDuration)
	}
	privacy := s.TikTokPrivacy
	if privacy == "" {
		privacy = "PUBLIC_TO_EVERYONE"
	}
	note := ""
	if !slices.Contains(info.PrivacyOptions, privacy) {
		// e.g. an app TikTok hasn't audited yet: only private.
		switch {
		case slices.Contains(info.PrivacyOptions, "SELF_ONLY"):
			privacy, note = "SELF_ONLY", "privado: TikTok solo lo permite así a esta app por ahora"
		case len(info.PrivacyOptions) > 0:
			privacy = info.PrivacyOptions[0]
		}
	}
	// The caption: title, description and tags as hashtags (if missing).
	caption := strings.TrimSpace(item.Title + "\n\n" + item.Description)
	for _, t := range item.Tags {
		h := "#" + strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(t), "#"), " ", "")
		if h != "#" && !strings.Contains(strings.ToLower(caption), strings.ToLower(h)) {
			caption += " " + h
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return uploadResult{}, err
	}
	defer f.Close()
	fi, _ := f.Stat()
	size := fi.Size()
	chunk, count := tiktokChunks(size)
	var init struct {
		PublishID string `json:"publish_id"`
		UploadURL string `json:"upload_url"`
	}
	err = u.acc.tiktokCall(ctx, "/v2/post/publish/video/init/", access, map[string]any{
		"post_info": map[string]any{"title": cut(caption, 2200), "privacy_level": privacy,
			"disable_comment": false, "disable_duet": false, "disable_stitch": false},
		"source_info": map[string]any{"source": "FILE_UPLOAD", "video_size": size, "chunk_size": chunk, "total_chunk_count": count},
	}, &init)
	if err != nil {
		return uploadResult{}, err
	}
	for i := 0; i < count; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if i == count-1 {
			end = size - 1 // the last one takes the rest
		}
		req, _ := http.NewRequestWithContext(ctx, "PUT", init.UploadURL, io.NewSectionReader(f, start, end-start+1))
		req.ContentLength = end - start + 1
		req.Header.Set("Content-Type", "video/mp4")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		res, err := u.acc.http.Do(req)
		if err != nil {
			return uploadResult{}, err
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 201 && res.StatusCode != 206 && res.StatusCode != 200 {
			return uploadResult{}, apiError(res.StatusCode, b)
		}
	}
	r := uploadResult{ID: init.PublishID, Msg: note}
	// TikTok processes it after the upload: wait a little to know how it went.
	deadline := time.Now().Add(u.wait)
	for {
		var st struct {
			Status     string   `json:"status"`
			FailReason string   `json:"fail_reason"`
			PostIDs    []string `json:"publicaly_available_post_id"`
		}
		if err := u.acc.tiktokCall(ctx, "/v2/post/publish/status/fetch/", access, map[string]any{"publish_id": init.PublishID}, &st); err != nil {
			return uploadResult{}, err
		}
		switch st.Status {
		case "PUBLISH_COMPLETE":
			if len(st.PostIDs) > 0 && info.Username != "" {
				r.URL = "https://www.tiktok.com/@" + info.Username + "/video/" + st.PostIDs[0]
			}
			return r, nil
		case "FAILED":
			return uploadResult{}, fmt.Errorf("TikTok no lo pudo publicar: %s", st.FailReason)
		}
		if time.Now().After(deadline) {
			r.Msg = strings.TrimPrefix(r.Msg+"; todavía procesándose en TikTok", "; ")
			return r, nil
		}
		select {
		case <-ctx.Done():
			return uploadResult{}, ctx.Err()
		case <-time.After(u.poll):
		}
	}
}

// ---------- HTTP ----------

func (srv *server) withAccounts(h func(http.ResponseWriter, *http.Request, *accounts)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.acc == nil {
			http.Error(w, "accounts not configured", http.StatusServiceUnavailable)
			return
		}
		h(w, r, srv.acc)
	}
}

// redirectURI is where the platform sends the browser back after the
// login (it must be registered exactly so): this server's callback, as the
// browser reaches it, or a page given in the environment that forwards to
// it (TikTok wants https: e.g. docs/tiktok/callback.html on GitHub Pages).
func redirectURI(r *http.Request, platform string) string {
	if u := os.Getenv(strings.ToUpper(platform) + "_REDIRECT_URI"); u != "" {
		return u
	}
	return "http://" + r.Host + "/api/publish/oauth/" + platform + "/callback"
}

func (srv *server) accountsOverview(w http.ResponseWriter, r *http.Request, a *accounts) {
	list := a.Status()
	out := make([]map[string]any, len(list))
	for i, s := range list {
		out[i] = map[string]any{"id": s.ID, "name": s.Name, "configured": s.Configured, "missing": s.Missing,
			"connected": s.Connected, "account": s.Account, "since": s.Since, "redirect": redirectURI(r, s.ID)}
	}
	writeJSON(w, out)
}

func (srv *server) accountConnect(w http.ResponseWriter, r *http.Request, a *accounts) {
	platform := r.PathValue("platform")
	u, err := a.AuthURL(platform, redirectURI(r, platform))
	if err != nil {
		pubError(w, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (srv *server) accountCallback(w http.ResponseWriter, r *http.Request, a *accounts) {
	platform := r.PathValue("platform")
	q := r.URL.Query()
	err := errors.New("no llegó el permiso")
	if e := q.Get("error"); e != "" {
		err = fmt.Errorf("%s: %s", e, q.Get("error_description"))
	} else if code := q.Get("code"); code != "" {
		err = a.Callback(r.Context(), platform, q.Get("state"), code)
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui;background:#11141b;color:#e8ebf2;padding:40px">
<h2>No se pudo conectar %s</h2><p>%s</p><p><a style="color:#6aa8ff" href="/publicar.html">← volver a Publicar</a></p>`,
			platformNames[platform], htmlEscape(err.Error()))
		return
	}
	http.Redirect(w, r, "/publicar.html?conectado="+url.QueryEscape(platform), http.StatusFound)
}

func (srv *server) accountDisconnect(w http.ResponseWriter, r *http.Request, a *accounts) {
	if err := a.Disconnect(r.PathValue("platform")); err != nil {
		pubError(w, err)
		return
	}
	srv.accountsOverview(w, r, a)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
