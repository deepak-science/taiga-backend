package routes_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/testsupport"
	"gitea.deepak.science/deepak/taiga/internal/weather"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

func (c *apiClient) export(query string) (*backup.Archive, map[string][]byte) {
	c.t.Helper()
	status, body := c.get("/export" + query)
	require.Equal(c.t, http.StatusOK, status, string(body))
	files := map[string][]byte{}
	a, err := backup.Read(bytes.NewReader(body), func(hash string, b []byte) error {
		files[hash] = b
		return nil
	})
	require.NoError(c.t, err)
	return a, files
}

func TestExport(t *testing.T) {
	srv, m, toker := newAPI(t)
	alice := newUserClient(t, srv, m, toker, "alice@example.com")
	bob := newUserClient(t, srv, m, toker, "bob@example.com")
	aliceToken, err := toker.DecodeTokenString(alice.token)
	require.NoError(t, err)

	splits := []testsupport.Split{{Meters: 2000, Seconds: 600}}
	firstFIT := testsupport.EncodeFIT(t, routeDay, splits)
	secondFIT := testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 1), splits)
	elsewhereFIT := testsupport.EncodeFITHeading(t, routeDay.AddDate(0, 0, 2), splits, 180)
	_, first := alice.uploadWorkout(firstFIT)
	_, second := alice.uploadWorkout(secondFIT)
	_, elsewhere := alice.uploadWorkout(elsewhereFIT)
	status, unparsed := alice.uploadFile([]byte("not a FIT file"))
	require.Equal(t, http.StatusCreated, status)
	bob.uploadWorkout(testsupport.EncodeFIT(t, routeDay.AddDate(0, 0, 5), splits))
	hash := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}

	require.NoError(t, m.DetectRoutes(t.Context(), aliceToken))
	var route models.RouteSummary
	{
		status, body := alice.get(fmt.Sprintf("/workouts/%d/route", first))
		require.Equal(t, http.StatusOK, status, string(body))
		require.NoError(t, json.Unmarshal(body, &route))
	}
	status, body := alice.put(fmt.Sprintf("/routes/%d", route.ID), `{"name":"Lakeshore"}`)
	require.Equal(t, http.StatusOK, status, string(body))
	status, body = alice.put(fmt.Sprintf("/workouts/%d/route/override", elsewhere), `{"route_id":null}`)
	require.Equal(t, http.StatusOK, status, string(body))
	status, body = alice.put(fmt.Sprintf("/workouts/%d/corrections", second), `{"excluded_reason":"ran with a friend"}`)
	require.Equal(t, http.StatusOK, status, string(body))
	status, body = alice.postJSON("/stretches", fmt.Sprintf(`{"name":"The hill","workout_id":%d,"start_m":500,"end_m":1500}`, first))
	require.Equal(t, http.StatusCreated, status, string(body))
	status, body = alice.postJSON("/workouts", `{"distance_miles": 3, "time_seconds": 1800}`)
	require.Equal(t, http.StatusCreated, status, string(body))

	fake := testsupport.NewFakeWeather(t)
	w, err := m.Workout(t.Context(), first, aliceToken)
	require.NoError(t, err)
	require.NoError(t, m.FetchWeather(t.Context(), first, weather.NewOpenMeteo(fake.URL()), w.EndTime.Time.Add(30*24*time.Hour), aliceToken))

	a, files := alice.export("")

	assert.Equal(t, backup.Format, a.Manifest.Format)
	assert.Equal(t, backup.Version, a.Manifest.Version)
	assert.Equal(t, workouts.ProcessorVersion, a.Manifest.ProcessorVersion)
	assert.True(t, a.Manifest.Fetched)
	require.NotNil(t, a.Manifest.Owner)
	assert.Equal(t, "alice@example.com", a.Manifest.Owner.Email)

	require.Len(t, a.Files, 4, "bob's isn't in it")
	assert.Equal(t, firstFIT, files[hash(firstFIT)])
	assert.Equal(t, []byte("not a FIT file"), files[hash([]byte("not a FIT file"))])
	made := map[string]bool{}
	for _, f := range a.Files {
		made[f.SHA256] = f.Workout
	}
	assert.Equal(t, map[string]bool{hash(firstFIT): true, hash(secondFIT): true, hash(elsewhereFIT): true, *unparsed.FileRepoHash: false}, made)

	require.Len(t, a.Workouts, 1)
	assert.Equal(t, 3.0, *a.Workouts[0].DistanceMiles)

	assert.Equal(t, []backup.Correction{{File: hash(secondFIT), ExcludedReason: "ran with a friend"}}, a.Corrections)

	require.Len(t, a.Stretches, 1)
	assert.Equal(t, "The hill", a.Stretches[0].Name)
	assert.Equal(t, hash(firstFIT), *a.Stretches[0].SourceFile)
	assert.NotEmpty(t, a.Stretches[0].Points)

	require.Len(t, a.Routes, 1)
	assert.Equal(t, "Lakeshore", *a.Routes[0].Name)
	assert.ElementsMatch(t, []string{hash(firstFIT), hash(secondFIT)}, a.Routes[0].Workouts)
	require.Len(t, a.RouteOverrides, 1)
	assert.Equal(t, backup.RouteOverride{File: hash(elsewhereFIT), CreatedAt: a.RouteOverrides[0].CreatedAt}, a.RouteOverrides[0], "kept off every route")

	require.NotEmpty(t, a.Weather)
	for _, r := range a.Weather {
		assert.Equal(t, hash(firstFIT), r.File)
	}

	t.Run("without what was fetched", func(t *testing.T) {
		a, _ := alice.export("?fetched=false")
		assert.False(t, a.Manifest.Fetched)
		assert.Empty(t, a.Weather)
		assert.Len(t, a.Files, 4)
	})

	t.Run("bob's own", func(t *testing.T) {
		a, _ := bob.export("")
		assert.Len(t, a.Files, 1)
		assert.Empty(t, a.Routes)
		assert.Empty(t, a.Stretches)
	})

	t.Run("a bad fetched", func(t *testing.T) {
		status, _ := alice.get("/export?fetched=maybe")
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("signed in only", func(t *testing.T) {
		anon := &apiClient{t: t, srv: srv}
		status, _ := anon.get("/export")
		assert.Equal(t, http.StatusUnauthorized, status)
	})

}
