// Package backup is taiga's archive of a user's data: their files, and what nothing can rebuild from them (docs/backup.md).
// Everything in it refers to workouts by their file's SHA-256 and never by database id, so an archive goes into any deployment.
package backup

import (
	"encoding/json"
	"time"
)

// Format names the archive in its manifest, so a tar of something else is turned away.
const Format = "taiga-backup"

// Version is the format this code writes; Read takes any version up to it.
const Version = 1

// The two kinds of archive: one user's export, and a whole deployment's backup.
const (
	KindUser       = "user"
	KindDeployment = "deployment"
)

// Manifest is the archive's first entry.
type Manifest struct {
	Format string `json:"format"`
	// Kind is KindUser, whose documents are at the top, or KindDeployment, whose accounts.json comes next and each account's documents under accounts/<key>/.
	Kind             string    `json:"kind"`
	Version          int       `json:"version"`
	ProcessorVersion string    `json:"processor_version"`
	CreatedAt        time.Time `json:"created_at"`
	// Fetched is false when the export left out weather and COROS's figures.
	Fetched bool `json:"fetched"`
	// Owner is whose export it is, for the reader; nothing signs in with it.
	Owner *Owner `json:"owner,omitempty"`
}

type Owner struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// Archive is everything an archive holds but the files' bytes.
type Archive struct {
	Manifest Manifest
	// Data is a user export's documents; empty in a deployment's.
	Data
	// Accounts is a deployment's; empty in a user export.
	Accounts []Account
}

// Data is one account's documents.
type Data struct {
	Files               []File
	Workouts            []Workout
	Corrections         []Correction
	Stretches           []Stretch
	Routes              []Route
	RouteOverrides      []RouteOverride
	SourceActivities    []SourceActivity
	SourceMatchAttempts []SourceMatchAttempt
	Weather             []Weather
	COROSMetadata       []COROSMetadata
}

// documents is each of the account's JSON entries, in the order they're written, under prefix.
func (d *Data) documents(prefix string) []document {
	return []document{
		{prefix + "files.json", &d.Files},
		{prefix + "workouts.json", &d.Workouts},
		{prefix + "corrections.json", &d.Corrections},
		{prefix + "stretches.json", &d.Stretches},
		{prefix + "routes.json", &d.Routes},
		{prefix + "route_overrides.json", &d.RouteOverrides},
		{prefix + "source_activities.json", &d.SourceActivities},
		{prefix + "source_match_attempts.json", &d.SourceMatchAttempts},
		{prefix + "weather.json", &d.Weather},
		{prefix + "coros_metadata.json", &d.COROSMetadata},
	}
}

// Account is one of a deployment's accounts, with what it takes to sign in to it again.
type Account struct {
	// Key names its directory, accounts/<key>/, within this archive only.
	Key         string `json:"key"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// Password is the stored bcrypt hash; null for an account that signs in only through a provider.
	Password      []byte     `json:"password"`
	EmailVerified bool       `json:"email_verified"`
	CreatedAt     time.Time  `json:"created_at"`
	Identities    []Identity `json:"identities"`
	Data          `json:"-"`
}

// Identity is a linked sign-in at an OpenID Connect provider.
type Identity struct {
	Issuer      string    `json:"issuer"`
	Subject     string    `json:"subject"`
	EmailAtLink string    `json:"email_at_link"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a *Archive) accountPrefix(key string) string { return "accounts/" + key + "/" }

// documents is every document after the manifest (and accounts.json), in the order they're written.
func (a *Archive) documents() []document {
	if a.Manifest.Kind != KindDeployment {
		return a.Data.documents("")
	}
	var docs []document
	for i := range a.Accounts {
		docs = append(docs, a.Accounts[i].documents(a.accountPrefix(a.Accounts[i].Key))...)
	}
	return docs
}

// AllFiles is every file the archive holds, each once.
func (a *Archive) AllFiles() []File {
	if a.Manifest.Kind != KindDeployment {
		return a.Files
	}
	seen := map[string]bool{}
	var files []File
	for _, acct := range a.Accounts {
		for _, f := range acct.Files {
			if !seen[f.SHA256] {
				seen[f.SHA256] = true
				files = append(files, f)
			}
		}
	}
	return files
}

type document struct {
	name string
	v    any
}

// File is an uploaded activity file; its bytes are the archive's files/<sha256>.
type File struct {
	SHA256    string    `json:"sha256"`
	Timestamp time.Time `json:"timestamp"`
	CreatedAt time.Time `json:"created_at"`
	// Workout says a workout was made from it, so a restore makes one again; false for a file whose workout the user deleted, or that failed to parse.
	Workout bool `json:"workout"`
}

// Workout is one entered by hand, with no file to rebuild it from.
type Workout struct {
	StartTime      *time.Time `json:"start_time"`
	EndTime        *time.Time `json:"end_time"`
	DistanceMiles  *float64   `json:"distance_miles"`
	TimeSeconds    *float64   `json:"time_seconds"`
	SpeedMph       *float64   `json:"speed_mph"`
	PaceMinPerMile *float64   `json:"pace_min_per_mile"`
	Sport          string     `json:"sport"`
	SubSport       string     `json:"sub_sport"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Correction is the user's say over a file-backed workout, which reprocessing applies over the file.
type Correction struct {
	File           string `json:"file"`
	Sport          string `json:"sport,omitempty"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
}

type Stretch struct {
	Name   string  `json:"name"`
	Sport  string  `json:"sport"`
	Meters float64 `json:"meters"`
	// Points is [lat, lon] pairs, as stored.
	Points json.RawMessage `json:"points"`
	// SourceFile is the file of the workout it was cut from; null once that workout is gone.
	SourceFile *string   `json:"source_file"`
	CreatedAt  time.Time `json:"created_at"`
}

// Route is a group of runs as detection last left it.
// Its runs are kept so a restore's detection keeps its name, which goes to the old route a new group overlaps most.
type Route struct {
	// Key is what RouteOverride.Route refers to it by, within this archive only.
	Key            string    `json:"key"`
	Name           *string   `json:"name"`
	Representative *string   `json:"representative"`
	Workouts       []string  `json:"workouts"`
	CreatedAt      time.Time `json:"created_at"`
}

// RouteOverride is the user's placement of one run.
type RouteOverride struct {
	File string `json:"file"`
	// Route is a Route's Key; null keeps the run off every route.
	Route     *string   `json:"route"`
	WithGroup bool      `json:"with_group"`
	CreatedAt time.Time `json:"created_at"`
}

// SourceActivity is an activity a sync has dealt with, so a restore doesn't fetch it again.
type SourceActivity struct {
	Kind          string     `json:"kind"`
	ExternalID    string     `json:"external_id"`
	File          *string    `json:"file"`
	StartTime     *time.Time `json:"start_time"`
	Files         int32      `json:"files"`
	FetchedAt     time.Time  `json:"fetched_at"`
	SportType     *int32     `json:"sport_type"`
	DetailTriedAt *time.Time `json:"detail_tried_at"`
}

// SourceMatchAttempt is a workout the walk back through a source's history looked for and didn't find.
type SourceMatchAttempt struct {
	File    string    `json:"file"`
	Kind    string    `json:"kind"`
	TriedAt time.Time `json:"tried_at"`
}

// Weather is one source's reading over a workout, with what it was asked.
type Weather struct {
	File            string     `json:"file"`
	Source          string     `json:"source"`
	FetchedAt       time.Time  `json:"fetched_at"`
	Latitude        float64    `json:"latitude"`
	Longitude       float64    `json:"longitude"`
	AskedStart      time.Time  `json:"asked_start"`
	AskedEnd        time.Time  `json:"asked_end"`
	Start           Conditions `json:"start"`
	End             Conditions `json:"end"`
	PrecipitationMM float64    `json:"precipitation_mm"`
}

type Conditions struct {
	TemperatureC     float64 `json:"temperature_c"`
	DewPointC        float64 `json:"dew_point_c"`
	HumidityPct      float64 `json:"humidity_pct"`
	WindSpeedMps     float64 `json:"wind_speed_mps"`
	WindDirectionDeg float64 `json:"wind_direction_deg"`
	CloudCoverPct    float64 `json:"cloud_cover_pct"`
}

// COROSMetadata is COROS's own figures for a workout, with the reply they were read from.
type COROSMetadata struct {
	File                string    `json:"file"`
	ExternalID          string    `json:"external_id"`
	FetchedAt           time.Time `json:"fetched_at"`
	Reply               string    `json:"reply"`
	WorkoutSeconds      *float64  `json:"workout_seconds"`
	TotalSeconds        *float64  `json:"total_seconds"`
	DistanceMeters      *float64  `json:"distance_meters"`
	AvgPaceSPerKm       *float64  `json:"avg_pace_s_per_km"`
	MovingAvgPaceSPerKm *float64  `json:"moving_avg_pace_s_per_km"`
	AdjustedPaceSPerKm  *float64  `json:"adjusted_pace_s_per_km"`
	BestKmSeconds       *float64  `json:"best_km_seconds"`
	AvgHeartRate        *float64  `json:"avg_heart_rate"`
	AvgCadence          *float64  `json:"avg_cadence"`
	AvgStrideLengthM    *float64  `json:"avg_stride_length_m"`
	AvgPowerWatts       *float64  `json:"avg_power_watts"`
	ElevationGainM      *float64  `json:"elevation_gain_m"`
	ElevationLossM      *float64  `json:"elevation_loss_m"`
	Calories            *float64  `json:"calories"`
	TrainingLoad        *float64  `json:"training_load"`
	AerobicTE           *float64  `json:"aerobic_te"`
	AnaerobicTE         *float64  `json:"anaerobic_te"`
	TrainingFocus       string    `json:"training_focus"`
	Performance         string    `json:"performance"`
}
