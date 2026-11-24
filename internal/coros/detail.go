package coros

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Detail is COROS's own account of an activity: what its app shows, some of it worked out by COROS and found nowhere in the file.
// A figure is nil where the reply doesn't give it, which goes by the sport.
type Detail struct {
	// Reply is what COROS said, whole.
	Reply string

	WorkoutSeconds *float64
	TotalSeconds   *float64
	DistanceMeters *float64
	// Paces are seconds a kilometre, whatever unit COROS gave them in.
	AvgPaceSecondsPerKm       *float64
	MovingAvgPaceSecondsPerKm *float64
	// AdjustedPaceSecondsPerKm is the pace on the flat that COROS reckons the effort was worth.
	AdjustedPaceSecondsPerKm *float64
	BestKmSeconds            *float64
	AvgHeartRate             *float64
	// AvgCadence is steps a minute for a run.
	AvgCadence            *float64
	AvgStrideLengthMeters *float64
	AvgPowerWatts         *float64
	ElevationGainMeters   *float64
	ElevationLossMeters   *float64
	Calories              *float64
	TrainingLoad          *float64
	AerobicTE             *float64
	AnaerobicTE           *float64
	// TrainingFocus and Performance are COROS's words, e.g. "Threshold" and "Best"; empty when it gives none.
	TrainingFocus string
	Performance   string
}

// The detail is prose like the list, a heading and then a line for each figure:
//
//	🏃 Outdoor Run Activity Details
//	========================================
//
//	Workout Time: 51:40
//	Distance: 7.63 km
//	Average Pace: 6:47 /km
//	Elevation Gain / Loss: 54 m / 51 m
//	Training Load: 203
//
// Which lines there are goes by the sport, so none is required, and a label not known here is passed over (the reply is kept whole).
// A known label with a value that can't be read is ErrUnexpectedReply, and so is a reply with no known label at all.
var (
	// Spaces and tabs only around the colon: \s would run an empty value on into the next line.
	detailLine   = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z][A-Za-z /]*?)[ \t]*:[ \t]*(.*?)[ \t]*$`)
	clockPattern = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)
	amount       = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\s*(\S*)$`)
)

const metersPerMile = 1609.344

func parseDetail(text string) (*Detail, error) {
	if !strings.Contains(text, "Activity Details") {
		return nil, fmt.Errorf("%w: not an activity's details: %s", ErrUnexpectedReply, brief(text))
	}
	d := &Detail{Reply: text}
	read := 0
	for _, line := range detailLine.FindAllStringSubmatch(text, -1) {
		label, value := line[1], line[2]
		var err error
		switch label {
		case "Workout Time":
			d.WorkoutSeconds, err = clock(value)
		case "Total Time":
			d.TotalSeconds, err = clock(value)
		case "Distance":
			d.DistanceMeters, err = unit(value, lengths)
		case "Average Pace":
			d.AvgPaceSecondsPerKm, err = pace(value)
		case "Moving Average Pace":
			d.MovingAvgPaceSecondsPerKm, err = pace(value)
		case "Adjusted Pace":
			d.AdjustedPaceSecondsPerKm, err = pace(value)
		case "Best Kilometer":
			d.BestKmSeconds, err = pace(value)
		case "Average Heart Rate":
			d.AvgHeartRate, err = unit(value, map[string]float64{"bpm": 1})
		case "Average Cadence":
			d.AvgCadence, err = unit(value, map[string]float64{"spm": 1, "rpm": 1})
		case "Average Stride Length":
			d.AvgStrideLengthMeters, err = unit(value, lengths)
		case "Average Power":
			d.AvgPowerWatts, err = unit(value, map[string]float64{"W": 1})
		case "Elevation Gain / Loss":
			gain, loss, both := strings.Cut(value, "/")
			if !both {
				err = fmt.Errorf("not a gain and a loss")
				break
			}
			if d.ElevationGainMeters, err = unit(strings.TrimSpace(gain), lengths); err == nil {
				d.ElevationLossMeters, err = unit(strings.TrimSpace(loss), lengths)
			}
		case "Calories":
			d.Calories, err = unit(value, map[string]float64{"kcal": 1})
		case "Training Load":
			d.TrainingLoad, err = unit(value, map[string]float64{"": 1})
		case "Aerobic TE":
			d.AerobicTE, err = unit(value, map[string]float64{"": 1})
		case "Anaerobic TE":
			d.AnaerobicTE, err = unit(value, map[string]float64{"": 1})
		case "Training Focus":
			d.TrainingFocus = word(value)
		case "Performance":
			d.Performance = word(value)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s %s: %v", ErrUnexpectedReply, label, brief(value), err)
		}
		read++
	}
	if read == 0 {
		return nil, fmt.Errorf("%w: an activity's details with nothing known in them: %s", ErrUnexpectedReply, brief(text))
	}
	return d, nil
}

var lengths = map[string]float64{"km": 1000, "m": 1, "cm": 0.01, "mi": metersPerMile, "ft": 0.3048}

// blank is how a figure COROS doesn't have might be written; none has been seen, so this is a guess that costs nothing.
func blank(value string) bool {
	return strings.Trim(value, "-– ") == ""
}

func word(value string) string {
	if blank(value) {
		return ""
	}
	return value
}

// clock reads "51:40" or "1:06:11" as seconds.
func clock(value string) (*float64, error) {
	if blank(value) {
		return nil, nil
	}
	m := clockPattern.FindStringSubmatch(value)
	if m == nil {
		return nil, fmt.Errorf("not a time")
	}
	hours := 0
	if m[1] != "" {
		var err error
		// More digits than an int holds is no time either.
		if hours, err = strconv.Atoi(m[1]); err != nil {
			return nil, fmt.Errorf("not a time")
		}
	}
	minutes, _ := strconv.Atoi(m[2])
	seconds, _ := strconv.Atoi(m[3])
	// "6:99" is a slip in the reply, not 7:39; minutes alone may run past the hour ("75:10").
	if seconds > 59 || (m[1] != "" && minutes > 59) {
		return nil, fmt.Errorf("not a time")
	}
	total := float64(hours*3600 + minutes*60 + seconds)
	return &total, nil
}

// pace reads "6:47 /km" as seconds a kilometre, turning a pace a mile into one.
func pace(value string) (*float64, error) {
	if blank(value) {
		return nil, nil
	}
	time, per, ok := strings.Cut(value, "/")
	if !ok {
		return nil, fmt.Errorf("not a pace")
	}
	seconds, err := clock(strings.TrimSpace(time))
	if err != nil || seconds == nil {
		return nil, fmt.Errorf("not a pace")
	}
	switch strings.TrimSpace(per) {
	case "km":
	case "mi":
		*seconds /= metersPerMile / 1000
	default:
		return nil, fmt.Errorf("a pace per %q", per)
	}
	return seconds, nil
}

// unit reads a number with one of the units given, scaled by it; a unit not listed is an error, since guessing would store a wrong figure.
func unit(value string, units map[string]float64) (*float64, error) {
	if blank(value) {
		return nil, nil
	}
	m := amount.FindStringSubmatch(value)
	if m == nil {
		return nil, fmt.Errorf("not a number")
	}
	scale, known := units[m[2]]
	if !known {
		return nil, fmt.Errorf("an unexpected unit %q", m[2])
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil, err
	}
	n *= scale
	return &n, nil
}

// Detail asks COROS for its own figures for the activity.
func (s *Session) Detail(ctx context.Context, a Activity) (*Detail, error) {
	text, failed, err := s.call(ctx, toolDetail, map[string]any{"labelId": a.ID, "sportType": a.SportType})
	if err != nil {
		return nil, err
	}
	if failed {
		return nil, fmt.Errorf("%w: activity %s: %s", ErrNoDetail, a.ID, brief(text))
	}
	return parseDetail(text)
}
