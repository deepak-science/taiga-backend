package workouts

import (
	"encoding/json"
	"math"
	"strconv"
)

// MaxSeriesPoints caps a stored series; plenty for a graph or a map track, and it keeps a marathon's 1s records small.
const MaxSeriesPoints = 2000

// Values is a series column; NaN (not recorded) encodes as null.
type Values []float64

func (v Values) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, len(v)*8+2)
	buf = append(buf, '[')
	for i, x := range v {
		if i > 0 {
			buf = append(buf, ',')
		}
		if math.IsNaN(x) || math.IsInf(x, 0) {
			buf = append(buf, "null"...)
			continue
		}
		buf = strconv.AppendFloat(buf, x, 'f', -1, 64)
	}
	return append(buf, ']'), nil
}

// Series is a workout's records as parallel columns, the shape graphing libraries take.
// A column the device never recorded (heart rate without a strap, position on a treadmill) is omitted.
type Series struct {
	TimeSeconds    Values `json:"time_s"`
	DistanceMeters Values `json:"distance_m"`
	SpeedMPS       Values `json:"speed_mps,omitempty"`
	AltitudeMeters Values `json:"altitude_m,omitempty"`
	HeartRate      Values `json:"heart_rate,omitempty"`
	Latitude       Values `json:"lat,omitempty"`
	Longitude      Values `json:"lon,omitempty"`
	// Cadence is steps a minute for a run or walk, revolutions a minute otherwise.
	Cadence Values `json:"cadence,omitempty"`
	// What a running watch records of each stride; EffortPaceMPS is COROS's grade-adjusted pace, as a speed.
	PowerWatts            Values `json:"power_w,omitempty"`
	EffortPaceMPS         Values `json:"effort_pace_mps,omitempty"`
	StepLengthMeters      Values `json:"step_length_m,omitempty"`
	VerticalOscillationMM Values `json:"vertical_oscillation_mm,omitempty"`
	GroundContactMS       Values `json:"ground_contact_ms,omitempty"`
	VerticalRatioPct      Values `json:"vertical_ratio_pct,omitempty"`
	// Walking is a run's walking breaks, warm-up and cool-down; nil when the records can't tell walking from running, or it isn't a run.
	Walking *Walking `json:"walking,omitempty"`
}

// BuildSeries samples the run's records evenly down to at most maxPoints, always keeping the last.
func BuildSeries(run *Run, maxPoints int) *Series {
	n := len(run.Records)
	stride := 1
	if n > maxPoints {
		stride = (n + maxPoints - 2) / (maxPoints - 1)
	}
	var picked []*RecordData
	for i := 0; i < n; i += stride {
		picked = append(picked, run.Records[i])
	}
	if n > 0 && (n-1)%stride != 0 {
		picked = append(picked, run.Records[n-1])
	}

	column := func(get func(*RecordData) float64, decimals int) Values {
		out := make(Values, len(picked))
		any := false
		for i, r := range picked {
			out[i] = round(get(r), decimals)
			any = any || !math.IsNaN(out[i])
		}
		if !any {
			return nil
		}
		return out
	}
	// On foot the file counts one foot's strides, and steps are what a runner means by cadence.
	// It goes by the sport as corrected, so a session the user turned into a run is counted like one.
	stepsPerStride := 1.0
	if Scored(run.Summary.Sport) {
		stepsPerStride = 2
	}
	s := &Series{
		TimeSeconds: column(func(r *RecordData) float64 { return r.DurationFromStart.Seconds() }, 1),
		DistanceMeters: column(func(r *RecordData) float64 {
			if r.Distance == nil {
				return math.NaN()
			}
			return r.Distance.Meters
		}, 1),
		SpeedMPS:       column(func(r *RecordData) float64 { return r.SpeedMeterPerSec }, 3),
		AltitudeMeters: column(func(r *RecordData) float64 { return r.AltitudeMeters }, 1),
		HeartRate:      column(func(r *RecordData) float64 { return r.HeartRate }, 0),
		// Six decimals of a degree is about 10cm.
		Latitude:  column(func(r *RecordData) float64 { return r.Latitude }, 6),
		Longitude: column(func(r *RecordData) float64 { return r.Longitude }, 6),
		Cadence:   column(func(r *RecordData) float64 { return r.Cadence * stepsPerStride }, 0),

		PowerWatts:            column(form(func(f *Form) float64 { return f.PowerWatts }), 0),
		EffortPaceMPS:         column(form(func(f *Form) float64 { return f.EffortPaceMPS }), 3),
		StepLengthMeters:      column(form(func(f *Form) float64 { return f.StepLengthMeters }), 2),
		VerticalOscillationMM: column(form(func(f *Form) float64 { return f.VerticalOscillationMM }), 1),
		GroundContactMS:       column(form(func(f *Form) float64 { return f.GroundContactMS }), 0),
		VerticalRatioPct:      column(form(func(f *Form) float64 { return f.VerticalRatioPct }), 1),
	}
	// A watch that measures no power in a sport writes 0 throughout, which is no column; a stop in a run that does have it is a real 0.
	if !anyPositive(s.PowerWatts) {
		s.PowerWatts = nil
	}
	if !anyPositive(s.EffortPaceMPS) {
		s.EffortPaceMPS = nil
	}
	// Time and distance are the x axes, so keep them present even when empty.
	if s.TimeSeconds == nil {
		s.TimeSeconds = Values{}
	}
	if s.DistanceMeters == nil {
		s.DistanceMeters = make(Values, len(picked))
		for i := range s.DistanceMeters {
			s.DistanceMeters[i] = math.NaN()
		}
	}
	if run.Summary.Sport == SportRunning {
		s.Walking = DetectWalking(s)
	}
	return s
}

func anyPositive(v Values) bool {
	for _, x := range v {
		if x > 0 {
			return true
		}
	}
	return false
}

// form reads a column out of the records' stride data, which only some formats have.
func form(get func(*Form) float64) func(*RecordData) float64 {
	return func(r *RecordData) float64 {
		if r.Form == nil {
			return math.NaN()
		}
		return get(r.Form)
	}
}

func round(x float64, decimals int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return math.NaN()
	}
	p := math.Pow(10, float64(decimals))
	return math.Round(x*p) / p
}

// UnmarshalJSON reads null back as NaN, so a stored series decodes to what was built.
func (v *Values) UnmarshalJSON(data []byte) error {
	var raw []*float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Values, len(raw))
	for i, x := range raw {
		out[i] = math.NaN()
		if x != nil {
			out[i] = *x
		}
	}
	*v = out
	return nil
}
