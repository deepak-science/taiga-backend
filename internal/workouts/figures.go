package workouts

import (
	"math"
	"strings"
	"time"

	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/proto"
)

// Figures are what a file states for a whole workout or one lap, as opposed to what's measured from its records; nil where it doesn't say.
// They're kept for tracking: best efforts, splits and routes never read them.
type Figures struct {
	// TimerSeconds is the watch's timer, which stops for pauses.
	TimerSeconds *float64 `json:"timer_seconds"`
	AvgHeartRate *float64 `json:"avg_heart_rate"`
	MaxHeartRate *float64 `json:"max_heart_rate"`
	MinHeartRate *float64 `json:"min_heart_rate"`
	AscentMeters *float64 `json:"ascent_meters"`
	// DescentMeters is positive.
	DescentMeters *float64 `json:"descent_meters"`
	Calories      *float64 `json:"calories"`
	// TotalSteps counts both feet; only Counted fills it, and only for a run or walk.
	TotalSteps *float64 `json:"total_steps"`
	// AvgCadence is as the file has it until Counted: revolutions a minute, which on foot is one foot's strides.
	AvgCadence    *float64 `json:"avg_cadence"`
	AvgPowerWatts *float64 `json:"avg_power_watts"`
	// AvgEffortPaceMPS is COROS's Effort Pace, its grade-adjusted pace, as a speed.
	AvgEffortPaceMPS         *float64 `json:"avg_effort_pace_mps"`
	AvgStepLengthMeters      *float64 `json:"avg_step_length_m"`
	AvgVerticalOscillationMM *float64 `json:"avg_vertical_oscillation_mm"`
	AvgGroundContactMS       *float64 `json:"avg_ground_contact_ms"`
	AvgVerticalRatioPct      *float64 `json:"avg_vertical_ratio_pct"`

	// cycles is the file's count: strides of one foot, or revolutions.
	cycles *float64
}

// Counted is the figures with strides turned into steps for a run or walk, as the series counts cadence.
// It goes by the sport as corrected; other sports keep their revolutions and have no steps.
func (f Figures) Counted(sport string) Figures {
	if !Scored(sport) {
		return f
	}
	double := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		twice := *v * 2
		return &twice
	}
	f.TotalSteps, f.AvgCadence = double(f.cycles), double(f.AvgCadence)
	return f
}

// Lap is one lap as the file recorded it: the watch's automatic ones, or the lap button's.
type Lap struct {
	StartOffsetSeconds float64  `json:"start_offset_seconds"`
	ElapsedSeconds     *float64 `json:"elapsed_seconds"`
	DistanceMeters     *float64 `json:"distance_meters"`
	// Trigger is why the lap ended as FIT names it, e.g. "manual" for the button or "distance"; empty when the file doesn't say.
	Trigger string `json:"trigger,omitempty"`
	Figures
}

// BuildLaps is the run's laps with their figures counted for its sport as corrected.
func BuildLaps(run *Run) []Lap {
	laps := make([]Lap, len(run.Laps))
	for i, lap := range run.Laps {
		lap.Figures = lap.Counted(run.Summary.Sport)
		laps[i] = lap
	}
	return laps
}

// Form is what a running watch records of each stride; NaN where it didn't.
type Form struct {
	PowerWatts float64
	// EffortPaceMPS is COROS's Effort Pace as a speed.
	EffortPaceMPS         float64
	StepLengthMeters      float64
	VerticalOscillationMM float64
	GroundContactMS       float64
	VerticalRatioPct      float64
}

// stated is a FIT value, or nil when it's the invalid marker (which the scaled getters turn into NaN).
func stated(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// measured is stated for a figure that can't be zero: COROS writes 0 for the stride it didn't measure, as when walking.
func measured(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return stated(v)
}

func orNaN8(v uint8) float64 {
	if v == basetype.Uint8Invalid {
		return math.NaN()
	}
	return float64(v)
}

func orNaN16(v uint16) float64 {
	if v == basetype.Uint16Invalid {
		return math.NaN()
	}
	return float64(v)
}

func orNaN32(v uint32) float64 {
	if v == basetype.Uint32Invalid {
		return math.NaN()
	}
	return float64(v)
}

// fractional is a whole cadence with its fraction (in 128ths) where the device writes one.
func fractional(whole, fraction uint8) float64 {
	if whole == basetype.Uint8Invalid {
		return math.NaN()
	}
	rpm := float64(whole)
	if fraction != basetype.Uint8Invalid {
		rpm += float64(fraction) / 128
	}
	return rpm
}

func nanIfZero(v float64) float64 {
	if v <= 0 {
		return math.NaN()
	}
	return v
}

// developerField finds one field a device added to the profile, by the name and units its description gives.
type developerField struct {
	index, num byte
	found      bool
}

// effortPace is COROS's name for its grade-adjusted pace, written on every record, lap and the session.
func effortPace(activity *filedef.Activity) developerField {
	for _, d := range activity.FieldDescriptions {
		if len(d.FieldName) > 0 && strings.EqualFold(strings.TrimSpace(d.FieldName[0]), "Effort Pace") && len(d.Units) > 0 && d.Units[0] == "m/s" {
			return developerField{index: d.DeveloperDataIndex, num: d.FieldDefinitionNumber, found: true}
		}
	}
	return developerField{}
}

// in reads the field out of a message's developer fields; NaN when it isn't there or isn't a number.
func (f developerField) in(fields []proto.DeveloperField) float64 {
	if !f.found {
		return math.NaN()
	}
	for _, field := range fields {
		if field.DeveloperDataIndex != f.index || field.Num != f.num {
			continue
		}
		switch field.Value.Type() {
		case proto.TypeFloat32:
			return float64(field.Value.Float32())
		case proto.TypeFloat64:
			return field.Value.Float64()
		}
	}
	return math.NaN()
}

func sessionFigures(s *mesgdef.Session, effort developerField) Figures {
	return Figures{
		TimerSeconds:  measured(s.TotalTimerTimeScaled()),
		AvgHeartRate:  measured(orNaN8(s.AvgHeartRate)),
		MaxHeartRate:  measured(orNaN8(s.MaxHeartRate)),
		MinHeartRate:  measured(orNaN8(s.MinHeartRate)),
		AscentMeters:  stated(orNaN16(s.TotalAscent)),
		DescentMeters: stated(orNaN16(s.TotalDescent)),
		Calories:      stated(orNaN16(s.TotalCalories)),
		AvgCadence:    measured(fractional(s.AvgCadence, s.AvgFractionalCadence)),
		// COROS writes 0 for a sport it measures no power in.
		AvgPowerWatts: measured(orNaN16(s.AvgPower)),

		AvgEffortPaceMPS: measured(effort.in(s.DeveloperFields)),
		// The file has millimetres.
		AvgStepLengthMeters:      measured(s.AvgStepLengthScaled() / 1000),
		AvgVerticalOscillationMM: measured(s.AvgVerticalOscillationScaled()),
		AvgGroundContactMS:       measured(s.AvgStanceTimeScaled()),
		AvgVerticalRatioPct:      measured(s.AvgVerticalRatioScaled()),
		cycles:                   stated(orNaN32(s.TotalCycles)),
	}
}

func lapFigures(l *mesgdef.Lap, effort developerField) Figures {
	return Figures{
		TimerSeconds:  measured(l.TotalTimerTimeScaled()),
		AvgHeartRate:  measured(orNaN8(l.AvgHeartRate)),
		MaxHeartRate:  measured(orNaN8(l.MaxHeartRate)),
		MinHeartRate:  measured(orNaN8(l.MinHeartRate)),
		AscentMeters:  stated(orNaN16(l.TotalAscent)),
		DescentMeters: stated(orNaN16(l.TotalDescent)),
		Calories:      stated(orNaN16(l.TotalCalories)),
		AvgCadence:    measured(fractional(l.AvgCadence, l.AvgFractionalCadence)),
		AvgPowerWatts: measured(orNaN16(l.AvgPower)),

		AvgEffortPaceMPS:         measured(effort.in(l.DeveloperFields)),
		AvgStepLengthMeters:      measured(l.AvgStepLengthScaled() / 1000),
		AvgVerticalOscillationMM: measured(l.AvgVerticalOscillationScaled()),
		AvgGroundContactMS:       measured(l.AvgStanceTimeScaled()),
		AvgVerticalRatioPct:      measured(l.AvgVerticalRatioScaled()),
		cycles:                   stated(orNaN32(l.TotalCycles)),
	}
}

// readLaps keeps the file's laps in the order it wrote them, each placed by its start against the workout's.
func readLaps(activity *filedef.Activity, start time.Time, effort developerField) []Lap {
	var laps []Lap
	for _, l := range activity.Laps {
		// Without a start there's no saying where in the workout it was.
		if l.StartTime.IsZero() {
			continue
		}
		lap := Lap{
			StartOffsetSeconds: l.StartTime.Sub(start).Seconds(),
			ElapsedSeconds:     measured(l.TotalElapsedTimeScaled()),
			DistanceMeters:     stated(l.TotalDistanceScaled()),
			Figures:            lapFigures(l, effort),
		}
		// A code the profile doesn't name prints as "LapTriggerInvalid(n)".
		if trigger := l.LapTrigger.String(); !strings.HasPrefix(trigger, "LapTrigger") {
			lap.Trigger = trigger
		}
		laps = append(laps, lap)
	}
	return laps
}

func recordForm(rec *mesgdef.Record, effort developerField) *Form {
	return &Form{
		PowerWatts:            orNaN16(rec.Power),
		EffortPaceMPS:         effort.in(rec.DeveloperFields),
		StepLengthMeters:      nanIfZero(rec.StepLengthScaled() / 1000),
		VerticalOscillationMM: nanIfZero(rec.VerticalOscillationScaled()),
		GroundContactMS:       nanIfZero(rec.StanceTimeScaled()),
		VerticalRatioPct:      nanIfZero(rec.VerticalRatioScaled()),
	}
}
