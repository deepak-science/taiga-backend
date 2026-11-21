package workouts

import (
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gitea.deepak.science/deepak/taiga/internal/places"
)

// locate fills in where the run started and its zone there, from its first GPS fix.
// A run with no fix keeps whatever offset its file stated, and has no place.
func (r *Run) locate() {
	for _, rec := range r.Records {
		// A TCX or Endomondo file can carry a fix that's off the globe; the next good one still says where it was.
		if !(math.Abs(rec.Latitude) <= 90) || !(math.Abs(rec.Longitude) <= 180) {
			continue
		}
		place := places.Lookup(rec.Latitude, rec.Longitude)
		if loc := place.Location(); loc != nil {
			_, offset := r.Summary.StartTime.In(loc).Zone()
			seconds := int32(offset)
			r.Summary.Place = place.Name
			r.Summary.Timezone = place.Timezone
			r.Summary.UTCOffsetSeconds = &seconds
		}
		return
	}
}

// statedOffset is the UTC offset a file gives as a local time beside the same instant in UTC, as FIT's activity message does; nil when it isn't one a clock could show.
func statedOffset(utc, local time.Time) *int32 {
	if utc.IsZero() || local.IsZero() {
		return nil
	}
	// Zones sit on quarter hours, from -12:00 to +14:00.
	offset := local.Sub(utc).Round(15 * time.Minute)
	if offset < -12*time.Hour || offset > 14*time.Hour {
		return nil
	}
	seconds := int32(offset / time.Second)
	return &seconds
}

// partsOfDay are named by the local hour each starts at, in order.
var partsOfDay = []struct {
	fromHour int
	name     string
}{
	{0, "late night"},
	{4, "early morning"},
	{7, "morning"},
	{11, "lunchtime"},
	{14, "afternoon"},
	{17, "evening"},
	{21, "night"},
}

func partOfDay(hour int) string {
	name := partsOfDay[0].name
	for _, p := range partsOfDay {
		if hour >= p.fromHour {
			name = p.name
		}
	}
	return name
}

// sportNouns is what one session of a sport is called; a sport not listed goes by its own name.
var sportNouns = map[string]string{
	"":                  "workout",
	"generic":           "workout",
	"training":          "workout",
	"fitness_equipment": "workout",
	SportRunning:        "run",
	SportWalking:        "walk",
	"cycling":           "ride",
	"e_biking":          "e-bike ride",
	"hiking":            "hike",
	"swimming":          "swim",
	"rowing":            "row",
}

// subSportNouns are the sub-sports that change what a session is called.
var subSportNouns = map[string]string{
	"treadmill":         "treadmill run",
	"indoor_running":    "treadmill run",
	"indoor_walking":    "indoor walk",
	"trail":             "trail run",
	"track":             "track run",
	"indoor_cycling":    "indoor ride",
	"spin":              "indoor ride",
	"strength_training": "strength workout",
}

// noun goes by the sport as corrected: a treadmill session corrected to a walk is a walk, not a treadmill run.
func (s *RunSummary) noun() string {
	if n, ok := subSportNouns[s.SubSport]; ok {
		switch {
		case strings.HasSuffix(n, " run") && s.Sport != SportRunning:
		case strings.HasSuffix(n, " ride") && s.Sport != "cycling":
		default:
			return n
		}
	}
	if n, ok := sportNouns[s.Sport]; ok {
		return n
	}
	return strings.ReplaceAll(s.Sport, "_", " ")
}

// Name is what to call the workout in a list: where or on what day, the part of the day, and what it was, e.g. "Madison afternoon run", or "Saturday morning treadmill run" with no place to go by.
// It's built only from the summary, so the same workout always reads the same; it doesn't tell two apart, which is what the start time beside it is for.
func (s *RunSummary) Name() string {
	noun := s.noun()
	if s.UTCOffsetSeconds == nil {
		// No zone, so no saying what part of whose day it was.
		return capitalise(noun)
	}
	local := s.StartTime.In(time.FixedZone("", int(*s.UTCOffsetSeconds)))
	lead := s.Place
	if lead == "" {
		lead = local.Weekday().String()
	}
	return lead + " " + partOfDay(local.Hour()) + " " + noun
}

func capitalise(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
