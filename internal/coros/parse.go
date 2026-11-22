package coros

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Activity is one entry of COROS's list.
type Activity struct {
	// ID is COROS's labelId.
	ID string
	// SportType is COROS's code for the sport (100 an outdoor run, 900 a walk); the file tool needs it back.
	SportType int
	// Name is the entry's heading, e.g. "Outdoor Run — 2026-11-21".
	Name string
	// Start and End are zero when the entry doesn't state them.
	Start, End time.Time
}

// The list is prose written for a language model, in this shape:
//
//	Sport Records — 2026-11-15 to 2026-11-21 (2 records)
//	========================
//
//	1. Outdoor Run — 2026-11-21
//	   Time Window: startTimestamp=1795286592 | endTimestamp=1795289692
//	   LabelId: 481919417381322856 | SportType: 100
//
// with more lines in each entry than these. Only the lines below are read, and anything that doesn't add up is ErrUnexpectedReply, so new wording fails the sync where "nothing new" would hide it.
var (
	countPattern = regexp.MustCompile(`\((\d+) records?\)`)
	entryPattern = regexp.MustCompile(`(?m)^\s*\d+\.\s+(.*\S)\s*$`)
	idPattern    = regexp.MustCompile(`LabelId:\s*(\d+)\s*\|\s*SportType:\s*(\d+)`)
	startPattern = regexp.MustCompile(`startTimestamp=(\d+)`)
	endPattern   = regexp.MustCompile(`endTimestamp=(\d+)`)
	linkPattern  = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
)

func parseActivities(text string) ([]Activity, error) {
	if strings.Contains(text, "No sport records found") {
		return nil, nil
	}
	count := countPattern.FindStringSubmatch(text)
	if count == nil {
		return nil, fmt.Errorf("%w: the list doesn't say how many activities it has: %q", ErrUnexpectedReply, brief(text))
	}
	want, _ := strconv.Atoi(count[1])

	var activities []Activity
	headings := entryPattern.FindAllStringSubmatchIndex(text, -1)
	for i, heading := range headings {
		end := len(text)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		entry := text[heading[0]:end]
		id := idPattern.FindStringSubmatch(entry)
		if id == nil {
			return nil, fmt.Errorf("%w: an activity without a LabelId and SportType: %q", ErrUnexpectedReply, brief(entry))
		}
		sportType, err := strconv.Atoi(id[2])
		if err != nil {
			return nil, fmt.Errorf("%w: sport type %q", ErrUnexpectedReply, id[2])
		}
		activities = append(activities, Activity{
			ID:        id[1],
			SportType: sportType,
			Name:      text[heading[2]:heading[3]],
			Start:     unixSeconds(startPattern.FindStringSubmatch(entry)),
			End:       unixSeconds(endPattern.FindStringSubmatch(entry)),
		})
	}
	if len(activities) != want {
		return nil, fmt.Errorf("%w: the list says %d activities and %d could be read", ErrUnexpectedReply, want, len(activities))
	}
	// COROS lists newest first.
	for i, j := 0, len(activities)-1; i < j; i, j = i+1, j-1 {
		activities[i], activities[j] = activities[j], activities[i]
	}
	return activities, nil
}

func unixSeconds(match []string) time.Time {
	if match == nil {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// fileURLs picks the download links out of the file tool's reply.
func fileURLs(text string) []string {
	return linkPattern.FindAllString(text, -1)
}
