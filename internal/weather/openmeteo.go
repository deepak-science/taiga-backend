package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OpenMeteoArchive is Open-Meteo's historical API: hourly values for any coordinates, no key for personal use.
const OpenMeteoArchive = "https://archive-api.open-meteo.com/v1/archive"

const (
	openMeteoTimeout = 30 * time.Second
	openMeteoDay     = "2006-01-02"
	openMeteoHour    = "2006-01-02T15:04"
)

// OpenMeteo answers both sources from the archive API: its default blend of models, and ERA5 asked for by name.
type OpenMeteo struct {
	BaseURL string
	Client  *http.Client
}

func NewOpenMeteo(baseURL string) *OpenMeteo {
	if baseURL == "" {
		baseURL = OpenMeteoArchive
	}
	return &OpenMeteo{BaseURL: baseURL, Client: &http.Client{Timeout: openMeteoTimeout}}
}

// The archive's hourly variables; precipitation at an hour is what fell in the hour before it.
var openMeteoHourly = []string{"temperature_2m", "dew_point_2m", "relative_humidity_2m", "wind_speed_10m", "wind_direction_10m", "cloud_cover", "precipitation"}

type openMeteoAnswer struct {
	Hourly map[string]json.RawMessage `json:"hourly"`
}

func (o *OpenMeteo) Lookup(ctx context.Context, source string, lat, lon float64, start, end time.Time) (*Reading, error) {
	start, end = start.UTC(), end.UTC()
	if end.Before(start) {
		end = start
	}
	query := url.Values{
		"latitude":  {strconv.FormatFloat(lat, 'f', -1, 64)},
		"longitude": {strconv.FormatFloat(lon, 'f', -1, 64)},
		// Whole days in UTC, wide enough for the rain before the start and the hour after the end.
		"start_date":      {start.Add(-PrecipitationLead).Format(openMeteoDay)},
		"end_date":        {end.Add(time.Hour).Format(openMeteoDay)},
		"hourly":          {strings.Join(openMeteoHourly, ",")},
		"wind_speed_unit": {"ms"},
	}
	switch source {
	case SourceDefault:
	case SourceERA5:
		query.Set("models", "era5")
	default:
		return nil, fmt.Errorf("open-meteo has no source %q", source)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking open-meteo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading open-meteo's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo answered %d: %.200s", resp.StatusCode, body)
	}
	var answer openMeteoAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("reading open-meteo's answer: %w", err)
	}
	hours, err := answer.hours()
	if err != nil {
		return nil, err
	}
	return hours.reading(start, end)
}

// hourly is the answer's columns against its times; nil where the source has no value.
type hourly struct {
	times  []time.Time
	values map[string][]*float64
}

func (a *openMeteoAnswer) hours() (*hourly, error) {
	var stamps []string
	if err := json.Unmarshal(a.Hourly["time"], &stamps); err != nil {
		return nil, fmt.Errorf("reading open-meteo's hours: %w", err)
	}
	h := &hourly{times: make([]time.Time, len(stamps)), values: map[string][]*float64{}}
	for i, s := range stamps {
		t, err := time.Parse(openMeteoHour, s)
		if err != nil {
			return nil, fmt.Errorf("reading open-meteo's hours: %w", err)
		}
		h.times[i] = t
	}
	for _, name := range openMeteoHourly {
		var column []*float64
		if err := json.Unmarshal(a.Hourly[name], &column); err != nil || len(column) != len(stamps) {
			return nil, fmt.Errorf("open-meteo's answer has no usable %s", name)
		}
		h.values[name] = column
	}
	return h, nil
}

func (h *hourly) reading(start, end time.Time) (*Reading, error) {
	r := &Reading{}
	var err error
	if r.Start, err = h.at(start); err != nil {
		return nil, err
	}
	if r.End, err = h.at(end); err != nil {
		return nil, err
	}
	from := start.Add(-PrecipitationLead)
	for i, t := range h.times {
		// The hour ending at t; the last one counted is the hour the workout ends in.
		if !t.After(from) || !t.Add(-time.Hour).Before(end) {
			continue
		}
		mm := h.values["precipitation"][i]
		if mm == nil {
			return nil, ErrNotYet
		}
		r.PrecipitationMM += *mm
	}
	return r, nil
}

// at interpolates between the hours either side of t.
func (h *hourly) at(t time.Time) (Conditions, error) {
	for i := 0; i+1 < len(h.times); i++ {
		if t.Before(h.times[i]) || t.After(h.times[i+1]) {
			continue
		}
		f := t.Sub(h.times[i]).Seconds() / h.times[i+1].Sub(h.times[i]).Seconds()
		value := func(name string, at int) float64 {
			if v := h.values[name][at]; v != nil {
				return *v
			}
			return math.NaN()
		}
		between := func(name string) float64 {
			return value(name, i) + (value(name, i+1)-value(name, i))*f
		}
		c := Conditions{
			TemperatureC:  between("temperature_2m"),
			DewPointC:     between("dew_point_2m"),
			HumidityPct:   between("relative_humidity_2m"),
			CloudCoverPct: between("cloud_cover"),
		}
		// Wind goes through its components, so 350° to 10° passes through north and not south.
		east := func(at int) float64 {
			return value("wind_speed_10m", at) * math.Sin(value("wind_direction_10m", at)*math.Pi/180)
		}
		north := func(at int) float64 {
			return value("wind_speed_10m", at) * math.Cos(value("wind_direction_10m", at)*math.Pi/180)
		}
		e, n := east(i)+(east(i+1)-east(i))*f, north(i)+(north(i+1)-north(i))*f
		c.WindSpeedMPS = math.Hypot(e, n)
		c.WindDirectionDeg = math.Mod(math.Atan2(e, n)*180/math.Pi+360, 360)
		for _, x := range []float64{c.TemperatureC, c.DewPointC, c.HumidityPct, c.CloudCoverPct, c.WindSpeedMPS, c.WindDirectionDeg} {
			if math.IsNaN(x) {
				return Conditions{}, ErrNotYet
			}
		}
		return c, nil
	}
	return Conditions{}, ErrNotYet
}
