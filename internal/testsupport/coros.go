package testsupport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FakeCOROS is an in-process stand-in for COROS's MCP server and the OAuth server in front of it: metadata, client registration, an authorize endpoint that signs in at once, tokens that rotate on refresh, the three tools taiga calls answering in COROS's own wording, and the links its files download from.
type FakeCOROS struct {
	Server *httptest.Server

	mu         sync.Mutex
	clients    map[string]string // id -> secret
	codes      map[string]fakeCode
	access     map[string]bool
	refresh    map[string]bool
	activities []FakeActivity
	seq        int

	// AccessTTL is how long an access token says it lasts; a second or less makes every session refresh first.
	AccessTTL time.Duration
	// DailyLimit is how many files it hands over before refusing; 0 for no limit.
	DailyLimit int
	// IssueSecret makes registration give the client a secret. COROS gives none, whatever is asked for, and so does the fake unless this is set.
	IssueSecret bool
	// DenyNext makes the next authorize send this OAuth error back instead of a code.
	DenyNext string
	// ListReply, when set, is what the list tool says instead of its list.
	ListReply string
	// Down makes the MCP endpoint answer 503 while sign-in and refreshing still work.
	Down bool
	// FailDownloads makes every file link answer 500, after it was issued and counted.
	FailDownloads bool
	// PlainList sends the list as plain text. COROS sends it as a JSON string, quotes and escapes included, and so does the fake unless this is set.
	PlainList bool

	Registrations int
	ListCalls     int
	// FileRequests counts links asked for, which is what COROS's limit counts.
	FileRequests int
	Downloads    int
	Revocations  int
	// DetailCalls counts the times an activity's own figures were asked for.
	DetailCalls int
	// DetailReply, when set, is what the detail tool says for every activity.
	DetailReply string
}

// FakeActivity is one entry of the fake account's history.
type FakeActivity struct {
	ID        string
	SportType int
	Name      string
	Start     time.Time
	End       time.Time
	// File is what its link downloads.
	File []byte
	// Parts is how many files it was recorded in; 0 is one.
	Parts int
	// InProgress makes the file tool say the activity hasn't finished.
	InProgress bool
	// Detail is what the detail tool says of it; empty for FakeRunDetail.
	Detail string
	// NoDetail makes the detail tool say it has nothing for the activity.
	NoDetail bool
}

// FakeRunDetail is the detail tool's reply for a run, in the wording COROS's server used in November 2026.
const FakeRunDetail = "🏃 Outdoor Run Activity Details\n========================================\n\nWorkout Time: 51:40\nDistance: 7.63 km\nTotal Time: 52:05\nAverage Pace: 6:47 /km\nMoving Average Pace: 6:38 /km\nAdjusted Pace: 6:43 /km\nBest Kilometer: 6:20 /km\nAverage Heart Rate: 150 bpm\nAverage Cadence: 170 spm\nAverage Stride Length: 1.05 m\nAverage Power: 250 W\nElevation Gain / Loss: 54 m / 51 m\nCalories: 300 kcal\nTraining Load: 120\nAerobic TE: 3.6\nAnaerobic TE: 0.9\nTraining Focus: Threshold\nPerformance: Best"

type fakeCode struct {
	client    string
	challenge string
	redirect  string
}

func NewFakeCOROS(t *testing.T) *FakeCOROS {
	t.Helper()
	f := &FakeCOROS{
		clients:   map[string]string{},
		codes:     map[string]fakeCode{},
		access:    map[string]bool{},
		refresh:   map[string]bool{},
		AccessTTL: 30 * 24 * time.Hour,
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-coros", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "querySportRecords"}, f.list)
	mcp.AddTool(server, &mcp.Tool{Name: "queryActivityFitFileDownloadUrls"}, f.fileURLs)
	mcp.AddTool(server, &mcp.Tool{Name: "getActivityDetail"}, f.detail)
	tools := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(r) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, f.Server.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		down := f.Down
		f.mu.Unlock()
		if down {
			http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
			return
		}
		tools.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", f.resourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", f.serverMetadata)
	mux.HandleFunc("POST /connect/register", f.register)
	mux.HandleFunc("GET /oauth2/authorize", f.authorize)
	mux.HandleFunc("POST /oauth2/token", f.token)
	mux.HandleFunc("POST /oauth2/revoke", f.revoke)
	mux.HandleFunc("GET /fit/{name}", f.download)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// Endpoint is the MCP address to configure.
func (f *FakeCOROS) Endpoint() string { return f.Server.URL + "/mcp" }

// Add puts activities in the account's history.
func (f *FakeCOROS) Add(activities ...FakeActivity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activities = append(f.activities, activities...)
}

// RevokeAll ends every grant, as the user removing taiga at COROS would.
func (f *FakeCOROS) RevokeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access, f.refresh = map[string]bool{}, map[string]bool{}
}

// Counts returns the counters under the lock: a sync may still be running.
func (f *FakeCOROS) Counts() (registrations, listCalls, fileRequests, downloads, revocations int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Registrations, f.ListCalls, f.FileRequests, f.Downloads, f.Revocations
}

func (f *FakeCOROS) authorized(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
}

func (f *FakeCOROS) next(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *FakeCOROS) resourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"resource":              f.Endpoint(),
		"authorization_servers": []string{f.Server.URL},
		"scopes_supported":      []string{"openid", "mcp.tools", "offline_access"},
	})
}

func (f *FakeCOROS) serverMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                           f.Server.URL,
		"authorization_endpoint":           f.Server.URL + "/oauth2/authorize",
		"token_endpoint":                   f.Server.URL + "/oauth2/token",
		"revocation_endpoint":              f.Server.URL + "/oauth2/revoke",
		"registration_endpoint":            f.Server.URL + "/connect/register",
		"response_types_supported":         []string{"code"},
		"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported": []string{"S256"},
		"scopes_supported":                 []string{"openid", "mcp.tools", "offline_access"},
	})
}

func (f *FakeCOROS) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.RedirectURIs) == 0 {
		http.Error(w, `{"error":"invalid_client_metadata"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Registrations++
	id := f.next("client")
	reply := map[string]any{"client_id": id, "redirect_uris": req.RedirectURIs, "client_name": req.ClientName, "token_endpoint_auth_method": "none"}
	secret := ""
	if f.IssueSecret {
		secret = f.next("secret")
		reply["client_secret"], reply["token_endpoint_auth_method"] = secret, "client_secret_basic"
	}
	f.clients[id] = secret
	writeJSONStatus(w, http.StatusCreated, reply)
}

func (f *FakeCOROS) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.clients[q.Get("client_id")]; !ok || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	params := url.Values{"state": {q.Get("state")}}
	if f.DenyNext != "" {
		params.Set("error", f.DenyNext)
		f.DenyNext = ""
	} else {
		code := f.next("code")
		f.codes[code] = fakeCode{client: q.Get("client_id"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
		params.Set("code", code)
	}
	back.RawQuery = params.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *FakeCOROS) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A client with a secret proves itself with it; one without only names itself, and PKCE does the rest.
	id, secret, basic := r.BasicAuth()
	id, _ = url.QueryUnescape(id)
	if !basic {
		id = r.FormValue("client_id")
	}
	want, known := f.clients[id]
	if !known || want != secret {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	fail := func() {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	}
	switch r.FormValue("grant_type") {
	case "authorization_code":
		code, ok := f.codes[r.FormValue("code")]
		delete(f.codes, r.FormValue("code"))
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if !ok || code.client != id || code.redirect != r.FormValue("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			fail()
			return
		}
	case "refresh_token":
		// Each refresh token works once: the one issued with the new access token replaces it.
		if !f.refresh[r.FormValue("refresh_token")] {
			fail()
			return
		}
		delete(f.refresh, r.FormValue("refresh_token"))
	default:
		fail()
		return
	}
	access, refresh := f.next("access"), f.next("refresh")
	f.access[access], f.refresh[refresh] = true, true
	writeJSON(w, map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    int(f.AccessTTL.Seconds()),
		"scope":         "mcp.tools offline_access",
	})
}

func (f *FakeCOROS) revoke(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Revocations++
	delete(f.refresh, r.FormValue("token"))
	// One grant at a time is all the tests make, so its access tokens go too.
	f.access = map[string]bool{}
}

type fakeListArgs struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Limit     int    `json:"limit"`
}

// list answers in the wording COROS's server used in November 2026.
func (f *FakeCOROS) list(_ context.Context, _ *mcp.CallToolRequest, args fakeListArgs) (*mcp.CallToolResult, any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ListCalls++
	if f.ListReply != "" {
		return text(f.ListReply), nil, nil
	}
	from, _ := time.Parse("20060102", args.StartDate)
	to, _ := time.Parse("20060102", args.EndDate)
	to = to.AddDate(0, 0, 1)
	var within []FakeActivity
	for _, a := range f.activities {
		if !a.Start.Before(from) && a.Start.Before(to) {
			within = append(within, a)
		}
	}
	sort.Slice(within, func(i, j int) bool { return within[i].Start.After(within[j].Start) })
	if args.Limit > 0 && len(within) > args.Limit {
		within = within[:args.Limit]
	}
	span := fmt.Sprintf("%s to %s", from.Format(time.DateOnly), to.AddDate(0, 0, -1).Format(time.DateOnly))
	if len(within) == 0 {
		return text("No sport records found from " + span + "."), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sport Records — %s (%d records)\n========================\n", span, len(within))
	for i, a := range within {
		name := a.Name
		if name == "" {
			name = "Outdoor Run"
		}
		fmt.Fprintf(&b, "\n%d. %s — %s\n   Location: Madison Run\n   Start Coordinates: 43.074001, -89.456001\n", i+1, name, a.Start.Format(time.DateOnly))
		fmt.Fprintf(&b, "   Time Window: startTimestamp=%d | endTimestamp=%d\n", a.Start.Unix(), a.End.Unix())
		b.WriteString("   Duration: 20:36 | Distance: 3.40 km\n   Average Pace: 6:04 /km | Avg HR: 185 bpm | Calories: 306 kcal\n")
		fmt.Fprintf(&b, "   LabelId: %s | SportType: %d\n", a.ID, a.SportType)
	}
	reply := b.String()
	if !f.PlainList {
		quoted, _ := json.Marshal(reply)
		reply = string(quoted)
	}
	return text(reply), nil, nil
}

type fakeFileArgs struct {
	LabelID   string `json:"labelId"`
	SportType int    `json:"sportType"`
	Limit     int    `json:"limit"`
}

func (f *FakeCOROS) fileURLs(_ context.Context, _ *mcp.CallToolRequest, args fakeFileArgs) (*mcp.CallToolResult, any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.activities {
		if a.ID != args.LabelID || a.SportType != args.SportType {
			continue
		}
		if a.InProgress {
			return text("This activity is still in progress; the FIT file is available after it ends."), nil, nil
		}
		if f.DailyLimit > 0 && f.FileRequests >= f.DailyLimit {
			result := text("You have reached the daily FIT file limit of 50 files. Please try again later.")
			result.IsError = true
			return result, nil, nil
		}
		parts, limit := max(a.Parts, 1), args.Limit
		if limit <= 0 {
			limit = 5
		}
		// COROS gives 5 unless asked for more, and never over 10.
		parts = min(parts, limit, 10)
		f.FileRequests += parts
		var b strings.Builder
		b.WriteString("Activity FIT file download URL(s):")
		for n := range parts {
			name := fmt.Sprintf("%s_f2935eeb%02d.fit", a.ID, n)
			fmt.Fprintf(&b, "\n%d. %s\n   %s/fit/%s", n+1, name, f.Server.URL, name)
		}
		return text(b.String()), nil, nil
	}
	result := text("No activity found for labelId " + args.LabelID + ".")
	result.IsError = true
	return result, nil, nil
}

type fakeDetailArgs struct {
	LabelID   string `json:"labelId"`
	SportType int    `json:"sportType"`
}

// detail answers as a JSON string, the way COROS sends it.
func (f *FakeCOROS) detail(_ context.Context, _ *mcp.CallToolRequest, args fakeDetailArgs) (*mcp.CallToolResult, any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DetailCalls++
	for _, a := range f.activities {
		if a.ID != args.LabelID || a.SportType != args.SportType || a.NoDetail {
			continue
		}
		reply := FakeRunDetail
		if a.Detail != "" {
			reply = a.Detail
		}
		if f.DetailReply != "" {
			reply = f.DetailReply
		}
		quoted, _ := json.Marshal(reply)
		return text(string(quoted)), nil, nil
	}
	result := text("No activity found for labelId " + args.LabelID + ".")
	result.IsError = true
	return result, nil, nil
}

func (f *FakeCOROS) download(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// As at COROS, the link is all it takes; a request that brings a token anyway is a leak worth failing on.
	if r.Header.Get("Authorization") != "" {
		http.Error(w, "the file link was sent a token", http.StatusBadRequest)
		return
	}
	if f.FailDownloads {
		http.Error(w, "storage is down", http.StatusInternalServerError)
		return
	}
	id, _, _ := strings.Cut(r.PathValue("name"), "_")
	for _, a := range f.activities {
		if a.ID == id {
			f.Downloads++
			_, _ = w.Write(a.File)
			return
		}
	}
	http.NotFound(w, r)
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Finish ends an activity that was in progress, so its file can be had.
func (f *FakeCOROS) Finish(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.activities {
		if f.activities[i].ID == id {
			f.activities[i].InProgress = false
		}
	}
}

// Details is how many times an activity's own figures were asked for, read under the lock.
func (f *FakeCOROS) Details() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.DetailCalls
}
