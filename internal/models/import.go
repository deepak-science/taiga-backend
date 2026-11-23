package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"gitea.deepak.science/deepak/taiga/internal/backup"
	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"gitea.deepak.science/deepak/taiga/internal/workouts"
)

// ImportReport is what bringing an archive into an account did; importing the same archive again reports nothing added or changed, though it lists the unreadable files again.
type ImportReport struct {
	// Files is how many the archive has, and FilesAdded how many of them the account didn't.
	Files      int `json:"files"`
	FilesAdded int `json:"files_added"`
	// FilesRetimed is files the account had with another timestamp, which now have the archive's.
	FilesRetimed int `json:"files_retimed"`
	// Workouts is how many were made, from files or entered by hand.
	Workouts    int `json:"workouts"`
	Corrections int `json:"corrections"`
	// Stretches is how many were added, or given the archive's points over the account's.
	Stretches int `json:"stretches"`
	// Routes is how many were added or renamed.
	Routes int `json:"routes"`
	// Unreadable is the files whose workout this taiga couldn't make, with why; they're kept as files without a workout.
	Unreadable map[string]string `json:"unreadable"`
}

// ImportAccount brings an archive's state into the signed-in account; the files' bytes must already be in fileRepo.
// Everything is matched by file hash, so it's idempotent, and where the account and the archive disagree over what the user set the archive wins.
// Nothing the account has and the archive doesn't is removed.
func (m *storeModel) ImportAccount(ctx context.Context, d *backup.Data, fileRepo filerepo.FileRepo, now time.Time, userToken *tokens.UserToken) (*ImportReport, error) {
	return m.importData(ctx, d, fileRepo, now, userToken)
}

// importData is ImportAccount, and restore's too, since an empty account matches nothing.
// Workouts are made from their files as an upload makes them, so their derived data is this taiga's; the rest is put back over them.
func (m *storeModel) importData(ctx context.Context, d *backup.Data, fileRepo filerepo.FileRepo, now time.Time, token *tokens.UserToken) (*ImportReport, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	userID := token.ID
	report := &ImportReport{Files: len(d.Files), Unreadable: map[string]string{}}
	before, err := q.ListWorkoutsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Stretches go first, so making each new workout times it over them.
	// A stretch made or reshaped here is timed over the workouts already there in the same transaction, so an import that stops can't leave it with its shape but not its efforts.
	var stretches *importedStretches
	if err := m.store.InTx(ctx, func(q db.Querier) error {
		var err error
		stretches, err = m.importStretches(ctx, q, d, before, token)
		if err != nil {
			return err
		}
		for _, s := range stretches.retime {
			if err := timeStretch(ctx, q, s, func(int32) bool { return true }, token); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	report.Stretches = len(stretches.retime)

	fileIDs := map[string]int32{}
	workoutIDs := map[string]int32{}
	for _, f := range d.Files {
		hash := f.SHA256
		af, err := q.GetActivityFileByHash(ctx, &db.GetActivityFileByHashParams{UserID: &userID, FileRepoHash: &hash})
		switch {
		case IsNotFound(err):
			af, err = q.CreateActivityFile(ctx, &db.CreateActivityFileParams{Timestamp: timestamptz(f.Timestamp), FileRepoHash: &hash, UserID: &userID})
			if IsNotFound(err) {
				// An upload or a sync added it since the lookup, and the insert gave way to it.
				af, err = q.GetActivityFileByHash(ctx, &db.GetActivityFileByHashParams{UserID: &userID, FileRepoHash: &hash})
				if err == nil && !sameInstant(af.Timestamp.Time, f.Timestamp) {
					af, err = q.UpdateActivityFile(ctx, &db.UpdateActivityFileParams{ID: af.ID, UserID: &userID, Timestamp: timestamptz(f.Timestamp), FileRepoHash: &hash})
					if err == nil {
						report.FilesRetimed++
					}
				}
			} else if err == nil {
				report.FilesAdded++
			}
			if err != nil {
				return nil, fmt.Errorf("file %s: %w", hash, err)
			}
		case err != nil:
			return nil, err
		case !sameInstant(af.Timestamp.Time, f.Timestamp):
			if _, err := q.UpdateActivityFile(ctx, &db.UpdateActivityFileParams{ID: af.ID, UserID: &userID, Timestamp: timestamptz(f.Timestamp), FileRepoHash: &hash}); err != nil {
				return nil, err
			}
			report.FilesRetimed++
		}
		fileIDs[hash] = af.ID
		if !f.Workout {
			continue
		}
		w, made, err := m.CreateWorkoutFromActivityFile(ctx, af.ID, token, fileRepo)
		if errors.Is(err, workouts.ErrUnreadableFile) || errors.Is(err, workouts.ErrUnsupportedActivity) {
			report.Unreadable[hash] = err.Error()
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("making the workout from %s: %w", hash, err)
		}
		workoutIDs[hash] = w.ID
		if made {
			report.Workouts++
		}
	}
	for _, w := range d.Workouts {
		if slices.ContainsFunc(before, func(have *db.Workout) bool { return sameHandEntered(have, &w) }) {
			continue
		}
		added, err := m.AddWorkout(ctx, &Workout{
			DistanceMiles: w.DistanceMiles, TimeSeconds: w.TimeSeconds, SpeedMph: w.SpeedMph, PaceMinPerMile: w.PaceMinPerMile,
			StartTime: optionalTimestamptz(w.StartTime), EndTime: optionalTimestamptz(w.EndTime), Sport: w.Sport, SubSport: w.SubSport,
		}, token)
		if err != nil {
			return nil, fmt.Errorf("hand-entered workout: %w", err)
		}
		// So the archive's own repeats collapse too, as they would importing it twice.
		before = append(before, added)
		report.Workouts++
	}
	for _, c := range d.Corrections {
		id, ok := workoutIDs[c.File]
		if !ok {
			continue
		}
		w, err := q.GetWorkout(ctx, &db.GetWorkoutParams{ID: id, UserID: userID})
		if err != nil {
			return nil, err
		}
		if w.SportOverride == c.Sport && w.UserExcludedReason == c.ExcludedReason {
			continue
		}
		if _, err := m.CorrectWorkout(ctx, id, Corrections{Sport: c.Sport, ExcludedReason: c.ExcludedReason}, token, fileRepo); err != nil {
			return nil, fmt.Errorf("correcting the workout from %s: %w", c.File, err)
		}
		report.Corrections++
	}
	ref := func(hash *string) *int32 {
		if hash == nil {
			return nil
		}
		if id, ok := workoutIDs[*hash]; ok {
			return &id
		}
		return nil
	}
	for i, id := range stretches.added {
		if src := ref(d.Stretches[i].SourceFile); id != 0 && src != nil {
			if err := q.SetStretchSourceWorkout(ctx, &db.SetStretchSourceWorkoutParams{ID: id, UserID: userID, SourceWorkoutID: src}); err != nil {
				return nil, err
			}
		}
	}

	// Under the lock detection takes, which rebuilds the memberships this adds to.
	if err := m.routesTx(ctx, userID, func(q db.Querier) error {
		n, err := importRoutes(ctx, q, d, userID, ref)
		report.Routes = n
		return err
	}); err != nil {
		return nil, err
	}
	if err := restoreSources(ctx, q, d, userID, fileIDs, workoutIDs); err != nil {
		return nil, err
	}
	// After every reprocess above, which drops a reading whose workout's span moved.
	if err := restoreFetched(ctx, q, d, userID, workoutIDs); err != nil {
		return nil, err
	}
	// Detection finds the duplicates and regroups the routes, keeping the names; the server's worker runs it.
	if err := q.EnqueueDetectRoutes(ctx, &db.EnqueueDetectRoutesParams{UserID: userID, RunAt: timestamptz(now)}); err != nil {
		return nil, err
	}
	return report, nil
}

// sameHandEntered is whether the account's workout is the archive's hand-entered one: they have no file, so they're matched by what was entered.
func sameHandEntered(have *db.Workout, w *backup.Workout) bool {
	return have.ActivityFileID == nil && have.Sport == w.Sport &&
		sameTime(have.StartTime, w.StartTime) && sameFloat(have.DistanceMiles, w.DistanceMiles) && sameFloat(have.TimeSeconds, w.TimeSeconds)
}

// sameTime goes to the second: an archive from SQLite has nanoseconds that Postgres would round away.
func sameTime(have pgtype.Timestamptz, t *time.Time) bool {
	if t == nil {
		return !have.Valid
	}
	return sameSecond(have, timestamptz(*t))
}

// sameInstant goes to the microsecond, which is all Postgres keeps of an archive's nanoseconds from SQLite.
func sameInstant(a, b time.Time) bool {
	return a.Sub(b).Abs() < time.Microsecond
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

type importedStretch struct {
	id    int32
	sport string
	shape workouts.Stretch
}

type importedStretches struct {
	// added is each archive stretch's id when this import made it (or an earlier one that stopped before setting its source), else 0, so its source can be set once the workouts exist.
	added []int32
	// retime is the stretches made or reshaped here, to time the workouts that were already there over.
	retime []importedStretch
}

// importStretches matches each of the archive's stretches to the account's by name, sport and the file of the workout it was cut from.
// One that matches with other points takes the archive's, and loses its efforts until they're timed again.
func (m *storeModel) importStretches(ctx context.Context, q db.Querier, d *backup.Data, before []*db.Workout, token *tokens.UserToken) (*importedStretches, error) {
	have, err := q.ListStretchesByUser(ctx, token.ID)
	if err != nil {
		return nil, err
	}
	files, err := q.ListActivityFilesByUser(ctx, &token.ID)
	if err != nil {
		return nil, err
	}
	fileHash := map[int32]string{}
	for _, f := range files {
		if f.FileRepoHash != nil {
			fileHash[f.ID] = *f.FileRepoHash
		}
	}
	workoutHash := map[int32]string{}
	for _, w := range before {
		if w.ActivityFileID != nil {
			workoutHash[w.ID] = fileHash[*w.ActivityFileID]
		}
	}
	source := func(s *db.Stretch) *string {
		if s.SourceWorkoutID == nil {
			return nil
		}
		if h, ok := workoutHash[*s.SourceWorkoutID]; ok {
			return &h
		}
		return nil
	}

	out := &importedStretches{added: make([]int32, len(d.Stretches))}
	used := map[int32]bool{}
	for i, s := range d.Stretches {
		var shape workouts.Stretch
		shape.Meters = s.Meters
		if err := json.Unmarshal(s.Points, &shape.Points); err != nil {
			return nil, fmt.Errorf("%w: stretch %q's points: %w", backup.ErrInvalid, s.Name, err)
		}
		// Fewer than two points is no ground at all; reshaping a stretch to it would leave it matching nothing.
		if len(shape.Points) < 2 {
			return nil, fmt.Errorf("%w: stretch %q has %d points", backup.ErrInvalid, s.Name, len(shape.Points))
		}
		decode := func(st *db.Stretch) (workouts.Stretch, error) {
			current := workouts.Stretch{Meters: st.Meters}
			if err := json.Unmarshal(st.Points, &current.Points); err != nil {
				return current, fmt.Errorf("decoding stretch %d: %w", st.ID, err)
			}
			return current, nil
		}
		j := slices.IndexFunc(have, func(st *db.Stretch) bool {
			src := source(st)
			return !used[st.ID] && st.Name == s.Name && st.Sport == s.Sport && (src == nil) == (s.SourceFile == nil) && (src == nil || *src == *s.SourceFile)
		})
		// An import that stopped before setting a new stretch's source left it with none: it's this one if the ground is the same.
		if j < 0 && s.SourceFile != nil {
			j = slices.IndexFunc(have, func(st *db.Stretch) bool {
				current, err := decode(st)
				return err == nil && !used[st.ID] && st.SourceWorkoutID == nil && st.Name == s.Name && st.Sport == s.Sport &&
					current.Meters == shape.Meters && slices.Equal(current.Points, shape.Points)
			})
			if j >= 0 {
				// It was timed when it was made, in the same transaction; only its source is left to set.
				used[have[j].ID] = true
				out.added[i] = have[j].ID
				continue
			}
		}
		if j < 0 {
			row, err := q.CreateStretch(ctx, &db.CreateStretchParams{UserID: token.ID, Name: s.Name, Sport: s.Sport, Meters: s.Meters, Points: s.Points})
			if err != nil {
				return nil, fmt.Errorf("stretch %q: %w", s.Name, err)
			}
			out.added[i] = row.ID
			out.retime = append(out.retime, importedStretch{id: row.ID, sport: s.Sport, shape: shape})
			continue
		}
		st := have[j]
		used[st.ID] = true
		current, err := decode(st)
		if err != nil {
			return nil, err
		}
		if current.Meters == shape.Meters && slices.Equal(current.Points, shape.Points) {
			continue
		}
		if err := q.DeleteStretchEffortsByStretch(ctx, &db.DeleteStretchEffortsByStretchParams{StretchID: st.ID, UserID: token.ID}); err != nil {
			return nil, err
		}
		if err := q.SetStretchShape(ctx, &db.SetStretchShapeParams{ID: st.ID, UserID: token.ID, Meters: s.Meters, Points: s.Points}); err != nil {
			return nil, err
		}
		out.retime = append(out.retime, importedStretch{id: st.ID, sport: s.Sport, shape: shape})
	}
	return out, nil
}

// importRoutes gives each of the archive's routes the account's route most of its runs are on, or a new one, so the detection after keeps its name.
// The archive's runs on no route yet join it; runs the account has on another route stay there, and detection regroups them all.
func importRoutes(ctx context.Context, q db.Querier, d *backup.Data, userID int32, ref func(*string) *int32) (int, error) {
	routes, err := q.ListRoutesByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	names := map[int32]*string{}
	for _, r := range routes {
		names[r.ID] = r.Name
	}
	memberships, err := q.ListWorkoutRoutesByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	on := map[int32]int32{}
	for _, wr := range memberships {
		on[wr.WorkoutID] = wr.RouteID
	}

	// Each archive route's runs, and the account's route most of them are on (0 for none), from the memberships before any change.
	type placement struct {
		runs    []int32
		target  int32
		overlap int
	}
	// A merged route's runs that the user put there carry a placement; the rest are its base, which detection groups it by.
	placedByUser := map[string]bool{}
	placedOnto := map[string]bool{}
	for _, o := range d.RouteOverrides {
		placedByUser[o.File] = true
		if o.Route != nil {
			placedOnto[*o.Route] = true
		}
	}
	// Detection groups only the runs with a track (what it reads, less duplicates), so those are the runs a route can keep.
	tracks, err := q.ListWorkoutTracksByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	tracked := map[int32]bool{}
	for _, t := range tracks {
		tracked[t.WorkoutID] = true
	}
	placed := make([]placement, len(d.Routes))
	for i, r := range d.Routes {
		overlap, base := map[int32]int{}, map[int32]int{}
		for _, hash := range r.Workouts {
			if id := ref(&hash); id != nil && tracked[*id] {
				placed[i].runs = append(placed[i].runs, *id)
				if rid, ok := on[*id]; ok {
					overlap[rid]++
					if !placedByUser[hash] {
						base[rid]++
					}
				}
			}
		}
		// The target is the account's route its base is on, so the archive's placements bring the merged runs to it: going by all its runs, a merged group bigger than the base would take the target, and the merge would point at itself.
		if len(base) > 0 {
			overlap = base
		}
		for rid, n := range overlap {
			if p := &placed[i]; p.target == 0 || n > p.overlap || (n == p.overlap && rid < p.target) {
				p.target, p.overlap = rid, n
			}
		}
	}
	// Archive routes the account has as one (it merged them, say) share a target: the one with most runs there names it, the first on a tie, so importing again doesn't rename it back and forth.
	namer := map[int32]int{}
	for i, p := range placed {
		if p.target == 0 {
			continue
		}
		if j, ok := namer[p.target]; !ok || p.overlap > placed[j].overlap {
			namer[p.target] = i
		}
	}

	changed := 0
	routeIDs := map[string]int32{}
	for i, r := range d.Routes {
		// Detection would delete a route with none of its runs tracked here (their files unreadable, or the workouts not counting), or one unnamed run nothing is placed onto, and another would be made on every import.
		if n := len(placed[i].runs); n == 0 || (n == 1 && r.Name == nil && !placedOnto[r.Key]) {
			continue
		}
		target := placed[i].target
		made := target == 0
		if made {
			row, err := q.CreateRoute(ctx, &db.CreateRouteParams{UserID: userID, RepresentativeWorkoutID: ref(r.Representative)})
			if err != nil {
				return 0, fmt.Errorf("route %s: %w", r.Key, err)
			}
			target = row.ID
		}
		// An unnamed route in the archive leaves the account's name: like a correction the archive doesn't have, it isn't one to take away.
		renamed := (made || namer[target] == i) && r.Name != nil && (names[target] == nil || *names[target] != *r.Name)
		if renamed {
			if _, err := q.RenameRoute(ctx, &db.RenameRouteParams{ID: target, UserID: userID, Name: r.Name}); err != nil {
				return 0, err
			}
			names[target] = r.Name
		}
		if made || renamed {
			changed++
		}
		routeIDs[r.Key] = target
		for _, id := range placed[i].runs {
			if _, ok := on[id]; ok {
				continue
			}
			if err := q.AddWorkoutRoute(ctx, &db.AddWorkoutRouteParams{WorkoutID: id, RouteID: target, UserID: userID}); err != nil {
				return 0, err
			}
			on[id] = target
		}
	}
	for _, o := range d.RouteOverrides {
		id := ref(&o.File)
		if id == nil {
			continue
		}
		var route *int32
		if o.Route != nil {
			rid, ok := routeIDs[*o.Route]
			if !ok {
				continue
			}
			route = &rid
		}
		if err := q.UpsertRouteOverride(ctx, &db.UpsertRouteOverrideParams{WorkoutID: *id, UserID: userID, RouteID: route, WithGroup: o.WithGroup}); err != nil {
			return 0, err
		}
	}
	return changed, nil
}
