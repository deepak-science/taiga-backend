package coros

import (
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// The testdata files are what COROS's server sent on 2026-11-21, byte for byte but for the ids, places and coordinates, which are made up.

func TestParseTheListAsCOROSSendsIt(t *testing.T) {
	raw, err := os.ReadFile("testdata/sport-records.txt")
	require.NoError(t, err)
	require.Equal(t, byte('"'), raw[0], "the list arrives as a JSON string, quotes and escaped newlines included")

	activities, err := parseActivities(unquote(string(raw)))
	require.NoError(t, err)
	require.Len(t, activities, 6)

	// Oldest first, though COROS lists newest first.
	assert.Equal(t, Activity{
		ID:        "400000000000005570",
		SportType: 100,
		Name:      "Outdoor Run — 2026-11-14",
		Start:     time.Unix(1794676869, 0).UTC(),
		End:       time.Unix(1794679092, 0).UTC(),
	}, activities[0])

	// An indoor session has no coordinates and a line break in the middle of its figures; neither matters.
	gym := activities[1]
	assert.Equal(t, "Gym Cardio — 2026-11-15", gym.Name)
	assert.Equal(t, 400, gym.SportType)
	assert.Equal(t, time.Unix(1794752085, 0).UTC(), gym.Start)

	assert.Equal(t, 1004, activities[2].SportType)
	assert.Equal(t, "Pickleball — 2026-11-18", activities[3].Name)
	assert.Equal(t, "400000000000002856", activities[5].ID)
}

func TestTheListUnquotedIsNotReadAsIs(t *testing.T) {
	raw, err := os.ReadFile("testdata/sport-records.txt")
	require.NoError(t, err)
	// Without undoing the quoting the whole reply is one line, and no entry starts one.
	_, err = parseActivities(string(raw))
	assert.ErrorIs(t, err, ErrUnexpectedReply)
}

func TestFileLinksAsCOROSSendsThem(t *testing.T) {
	raw, err := os.ReadFile("testdata/fit-urls.txt")
	require.NoError(t, err)
	// Plain text, unlike the list.
	assert.Equal(t, string(raw), unquote(string(raw)))
	assert.Equal(t, []string{"https://s3.coros.com/fit/v2/400000000000000000/400000000000001234_0123456789.fit"}, fileURLs(string(raw)))
}

func TestBriefKeepsLinksOut(t *testing.T) {
	raw, err := os.ReadFile("testdata/fit-urls.txt")
	require.NoError(t, err)
	assert.NotContains(t, brief(string(raw)), "s3.coros.com", "a link works for anyone who has it, so it can't go into an error or a log")
}

func TestAnActivityNamedLikeTheEmptyReplyIsStillListed(t *testing.T) {
	list := "Sport Records — 2026-11-15 to 2026-11-21 (1 records)\n========================\n\n1. No sport records found — 2026-11-21\n   Location: No sport records found\n   Time Window: startTimestamp=1795286592 | endTimestamp=1795289692\n   LabelId: 5 | SportType: 100\n"
	activities, err := parseActivities(list)
	require.NoError(t, err)
	require.Len(t, activities, 1)
	assert.Equal(t, "5", activities[0].ID)

	activities, err = parseActivities("No sport records found from 2024-01-01 to 2026-11-21.")
	require.NoError(t, err)
	assert.Empty(t, activities)
}

func TestBriefCutsBetweenCharacters(t *testing.T) {
	// Two bytes each, and an odd one first so the cut would fall inside one.
	cut := brief("a" + strings.Repeat("é", 300))
	assert.True(t, utf8.ValidString(cut))
	assert.True(t, strings.HasSuffix(cut, "…"))

	assert.True(t, utf8.ValidString(brief("caf\xc3")), "what COROS sends broken is mended too")
}

func TestEveryEndpointHasToBeAsProtectedAsTheServer(t *testing.T) {
	s := New("https://mcp.example.com/mcp", "https://taiga.example.com/api/sources/coros/callback")
	client := Client{
		AuthURL:         "https://mcp.example.com/oauth2/authorize",
		TokenURL:        "https://mcp.example.com/oauth2/token",
		RevocationURL:   "https://mcp.example.com/oauth2/revoke",
		RegistrationURL: "https://mcp.example.com/connect/register",
	}
	require.NoError(t, s.checkEndpoints(&client))

	// The SDK lets this one through, and it's sent the refresh token.
	client.RevocationURL = "http://mcp.example.com/oauth2/revoke"
	require.Error(t, s.checkEndpoints(&client))
	assert.Error(t, s.Revoke(t.Context(), &client, &oauth2.Token{RefreshToken: "refresh"}), "nor is a registration kept from before the check sent one")

	// A server with no revocation endpoint is fine.
	client.RevocationURL = ""
	assert.NoError(t, s.checkEndpoints(&client))
}
