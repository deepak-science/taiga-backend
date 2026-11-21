package testsupport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// FakeWeather stands in for Open-Meteo's archive API, answering every hour of the days asked for.
// Temperature is the hour of the day (one more from the default blend than from ERA5), dew point 5 under it, humidity 70, cloud cover 40, wind 3 m/s turning from 350° on even hours to 10° on odd, and 0.5mm of rain an hour.
type FakeWeather struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []url.Values
	// Empty answers with nulls, as the archive does for hours it doesn't have yet.
	empty bool
	// status, when set, is answered instead.
	status int
}

func NewFakeWeather(t *testing.T) *FakeWeather {
	t.Helper()
	f := &FakeWeather{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.archive))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *FakeWeather) URL() string { return f.Server.URL }

// Requests are the queries received so far.
func (f *FakeWeather) Requests() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.requests...)
}

func (f *FakeWeather) SetEmpty(empty bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.empty = empty
}

func (f *FakeWeather) SetStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *FakeWeather) archive(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	query := r.URL.Query()
	f.requests = append(f.requests, query)
	empty, status := f.empty, f.status
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, "the fake is failing", status)
		return
	}
	first, err1 := time.Parse("2006-01-02", query.Get("start_date"))
	last, err2 := time.Parse("2006-01-02", query.Get("end_date"))
	if err1 != nil || err2 != nil {
		http.Error(w, `{"error":true,"reason":"bad dates"}`, http.StatusBadRequest)
		return
	}
	warmer := 1.0
	if query.Get("models") == "era5" {
		warmer = 0
	}
	hourly := map[string][]any{}
	add := func(name string, value float64) {
		if empty {
			hourly[name] = append(hourly[name], nil)
			return
		}
		hourly[name] = append(hourly[name], value)
	}
	for t := first; t.Before(last.Add(24 * time.Hour)); t = t.Add(time.Hour) {
		hourly["time"] = append(hourly["time"], t.Format("2006-01-02T15:04"))
		temperature := float64(t.Hour()) + warmer
		add("temperature_2m", temperature)
		add("dew_point_2m", temperature-5)
		add("relative_humidity_2m", 70)
		add("cloud_cover", 40)
		add("wind_speed_10m", 3)
		direction := 350.0
		if t.Hour()%2 == 1 {
			direction = 10
		}
		add("wind_direction_10m", direction)
		add("precipitation", 0.5)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"hourly": hourly})
}
