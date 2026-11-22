package testsupport

import (
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// EncodeFITFromWatch is EncodeFIT with what a COROS running watch writes besides: the session's own totals and averages, two laps (the first ended with the lap button a third of the way in), and on every record power, the stride's measurements and "Effort Pace", a field COROS adds to the profile.
// The first record has zeros for the stride, which is how the watch writes one it didn't measure.
//
// The session says: timer 30s short of elapsed, heart rate 150 average, 172 max, 118 min, 12m up and 8m down, 300 kcal, 1000 strides at 85 a minute, 250W, effort pace 2.9 m/s, steps of 1.1m, 9.5cm of bounce, 280ms on the ground, a vertical ratio of 8.5%.
// Each record says 250W, 3 m/s effort pace, and the same stride.
func EncodeFITFromWatch(t *testing.T, start time.Time, splits []Split) []byte {
	t.Helper()
	activity := buildActivity(start, splits, 0)
	effort := func(mps float32) proto.DeveloperField {
		return proto.DeveloperField{DeveloperDataIndex: 0, Num: 16, Value: proto.Float32(mps)}
	}
	activity.DeveloperDataIds = append(activity.DeveloperDataIds, mesgdef.NewDeveloperDataId(nil).
		SetDeveloperDataIndex(0).
		SetManufacturerId(typedef.ManufacturerCoros))
	activity.FieldDescriptions = append(activity.FieldDescriptions, mesgdef.NewFieldDescription(nil).
		SetDeveloperDataIndex(0).
		SetFieldDefinitionNumber(16).
		SetFitBaseTypeId(basetype.Float32).
		SetFieldName([]string{"Effort Pace"}).
		SetUnits([]string{"m/s"}))

	for i, record := range activity.Records {
		record.SetPower(250).SetStepLengthScaled(1100).SetDeveloperFields(effort(3))
		if i == 0 {
			record.SetVerticalOscillationScaled(0).SetStanceTimeScaled(0).SetVerticalRatioScaled(0)
			continue
		}
		record.SetVerticalOscillationScaled(95).SetStanceTimeScaled(280).SetVerticalRatioScaled(8.5)
	}

	session := activity.Sessions[0]
	elapsed, meters := session.TotalElapsedTimeScaled(), session.TotalDistanceScaled()
	session.
		SetTotalTimerTimeScaled(elapsed - 30).
		SetAvgHeartRate(150).SetMaxHeartRate(172).SetMinHeartRate(118).
		SetTotalAscent(12).SetTotalDescent(8).
		SetTotalCalories(300).
		SetTotalCycles(1000).SetAvgCadence(85).
		SetAvgPower(250).
		SetAvgStepLengthScaled(1100).SetAvgVerticalOscillationScaled(95).SetAvgStanceTimeScaled(280).SetAvgVerticalRatioScaled(8.5).
		SetDeveloperFields(effort(2.9))

	third := time.Duration(elapsed / 3 * float64(time.Second)).Truncate(time.Second)
	activity.Laps = append(activity.Laps,
		mesgdef.NewLap(nil).
			SetStartTime(start).SetTimestamp(start.Add(third)).
			SetTotalElapsedTimeScaled(third.Seconds()).SetTotalTimerTimeScaled(third.Seconds()).
			SetTotalDistanceScaled(meters/3).
			SetLapTrigger(typedef.LapTriggerManual).
			SetAvgHeartRate(140).SetMaxHeartRate(155).SetMinHeartRate(118).
			SetAvgCadence(84).SetAvgPower(240).
			SetAvgStanceTimeScaled(275).
			SetDeveloperFields(effort(2.8)),
		mesgdef.NewLap(nil).
			SetStartTime(start.Add(third)).SetTimestamp(session.Timestamp).
			SetTotalElapsedTimeScaled(elapsed-third.Seconds()).
			SetTotalDistanceScaled(meters*2/3).
			SetLapTrigger(typedef.LapTriggerSessionEnd).
			SetAvgHeartRate(155).
			SetAvgStanceTimeScaled(283))
	// Developer fields came with the second version of the protocol.
	return encode(t, activity, encoder.WithProtocolVersion(proto.V2))
}
