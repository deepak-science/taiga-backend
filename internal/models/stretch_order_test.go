package models

import "testing"

func TestAlongRoute(t *testing.T) {
	rep := int32(1)
	for name, c := range map[string]struct {
		firstPass      map[int32]float64
		representative *int32
		want           float64
	}{
		"the representative's own pass":        {map[int32]float64{1: 300, 2: 900, 3: 950}, &rep, 300},
		"the middle run when it has none":      {map[int32]float64{2: 100, 3: 320, 4: 2000}, &rep, 320},
		"the middle run without one to go by":  {map[int32]float64{2: 100, 3: 320, 4: 2000}, nil, 320},
		"nothing to go by":                     {map[int32]float64{}, &rep, 0},
		"one run that started its watch early": {map[int32]float64{2: 700}, nil, 700},
	} {
		if got := alongRoute(c.firstPass, c.representative); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
