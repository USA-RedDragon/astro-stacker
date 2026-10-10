package tslink

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Target struct {
	ID          int
	GUID        string
	Name        string
	ProjectID   int
	ProjectGUID string
	Project     string
	IsMosaic    bool
	MinAltitude float64
	Active      bool
	RA          float64
	Dec         float64
	Rotation    float64
}

type Project struct {
	ID          int
	GUID        string
	Name        string
	IsMosaic    bool
	MinAltitude float64
	Targets     []Target
}

func Targets(ctx context.Context, sched *gorm.DB) ([]Target, error) {
	var rows []struct {
		ID          int
		GUID        *string
		Name        string
		ProjectID   int
		ProjectGUID *string
		Project     string
		IsMosaic    *int
		MinAltitude *float64
		Active      *int
		RA          *float64
		Dec         *float64
		Rotation    *float64
	}
	if err := sched.WithContext(ctx).Table("target").
		Select(`target."Id" AS id, target.guid AS guid, target.name AS name, project."Id" AS project_id, project.guid AS project_guid, ` +
			`project.name AS project, project."isMosaic" AS is_mosaic, project.minimumaltitude AS min_altitude, target.active AS active, ` +
			`target.ra AS ra, target.dec AS dec, target.rotation AS rotation`).
		Joins(`JOIN project ON target.projectid = project."Id"`).
		Order(`target."Id"`).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load targets: %w", err)
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		t := Target{ID: r.ID, Name: r.Name, ProjectID: r.ProjectID, Project: r.Project}
		t.GUID, t.ProjectGUID = deref(r.GUID), deref(r.ProjectGUID)
		t.IsMosaic = r.IsMosaic != nil && *r.IsMosaic == 1
		t.Active = r.Active == nil || *r.Active == 1
		if r.MinAltitude != nil {
			t.MinAltitude = *r.MinAltitude
		}
		if r.RA != nil {
			t.RA = *r.RA * 15
		}
		if r.Dec != nil {
			t.Dec = *r.Dec
		}
		if r.Rotation != nil {
			t.Rotation = *r.Rotation
		}
		out = append(out, t)
	}
	return out, nil
}

func Projects(ctx context.Context, sched *gorm.DB) ([]Project, error) {
	targets, err := Targets(ctx, sched)
	if err != nil {
		return nil, err
	}
	return GroupProjects(targets), nil
}

func GroupProjects(targets []Target) []Project {
	byID := map[int]*Project{}
	var order []int
	for _, t := range targets {
		p, ok := byID[t.ProjectID]
		if !ok {
			p = &Project{ID: t.ProjectID, GUID: t.ProjectGUID, Name: t.Project, IsMosaic: t.IsMosaic, MinAltitude: t.MinAltitude}
			byID[t.ProjectID] = p
			order = append(order, t.ProjectID)
		}
		p.Targets = append(p.Targets, t)
	}
	out := make([]Project, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	slices.SortFunc(out, func(a, b Project) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type Linker struct {
	mu    sync.Mutex
	files map[int]string
}

type Stats struct {
	Header        int
	AcquiredImage int
	Name          int
	Unlinked      int
}

type unlinked struct {
	ID        int
	Key       string
	Object    string
	TSTarget  *string
	TSProject *string
}

const metadataChunk = 500

func (l *Linker) Link(ctx context.Context, appDB, sched *gorm.DB) (Stats, error) {
	var st Stats
	var frames []unlinked
	if err := appDB.WithContext(ctx).Table("frames f").
		Select("f.id, f.key, f.object, f.ts_target, f.ts_project").
		Joins("LEFT JOIN frame_targets ft ON ft.frame_id = f.id").
		Where("ft.id IS NULL AND f.type = ? AND f.index_error IS NULL", "LIGHT").
		Order("f.id").Scan(&frames).Error; err != nil {
		return st, fmt.Errorf("load unlinked frames: %w", err)
	}
	if len(frames) == 0 {
		return st, nil
	}
	targets, err := Targets(ctx, sched)
	if err != nil {
		return st, err
	}
	byGUID := map[string]Target{}
	byID := map[int]Target{}
	nameCount := map[string]int{}
	byName := map[string]Target{}
	for _, t := range targets {
		byID[t.ID] = t
		if t.GUID != "" {
			byGUID[t.GUID] = t
		}
		nameCount[t.Name]++
		byName[t.Name] = t
	}
	var images map[string][]Target
	now := time.Now()
	var links []app.FrameTarget
	for _, f := range frames {
		link := app.FrameTarget{FrameID: f.ID, Object: f.Object, LinkedAt: now}
		switch {
		case f.TSTarget != nil && *f.TSTarget != "":
			link.TargetGUID, link.Method = *f.TSTarget, app.LinkHeader
			link.ProjectGUID = deref(f.TSProject)
			if t, ok := byGUID[link.TargetGUID]; ok && link.ProjectGUID == "" {
				link.ProjectGUID = t.ProjectGUID
			}
			st.Header++
		default:
			if images == nil {
				if images, err = l.imagesByFile(ctx, sched, byID); err != nil {
					return st, err
				}
			}
			if t, ok := pickImage(images[path.Base(f.Key)], f.Object); ok && t.GUID != "" {
				link.TargetGUID, link.ProjectGUID, link.Method = t.GUID, t.ProjectGUID, app.LinkAcquiredImage
				st.AcquiredImage++
			} else if t, ok := byName[f.Object]; ok && nameCount[f.Object] == 1 && t.GUID != "" {
				link.TargetGUID, link.ProjectGUID, link.Method = t.GUID, t.ProjectGUID, app.LinkName
				st.Name++
			} else {
				st.Unlinked++
				continue
			}
		}
		links = append(links, link)
	}
	for i := 0; i < len(links); i += metadataChunk {
		batch := links[i:min(i+metadataChunk, len(links))]
		if err := appDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error; err != nil {
			return st, fmt.Errorf("save frame links: %w", err)
		}
	}
	return st, nil
}

func pickImage(cands []Target, object string) (Target, bool) {
	if len(cands) == 0 {
		return Target{}, false
	}
	for _, t := range cands {
		if t.Name == object {
			return t, true
		}
	}
	first := cands[0]
	for _, t := range cands[1:] {
		if t.GUID != first.GUID {
			return Target{}, false
		}
	}
	return first, true
}

func (l *Linker) imagesByFile(ctx context.Context, sched *gorm.DB, byID map[int]Target) (map[string][]Target, error) {
	var rows []struct {
		ID       int
		TargetID *int
	}
	if err := sched.WithContext(ctx).Table("acquiredimage").Select(`"Id" AS id, "targetId" AS target_id`).Order(`"Id"`).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load acquired images: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.files == nil {
		l.files = map[int]string{}
	}
	var missing []int
	for _, r := range rows {
		if _, ok := l.files[r.ID]; !ok {
			missing = append(missing, r.ID)
		}
	}
	for i := 0; i < len(missing); i += metadataChunk {
		ids := missing[i:min(i+metadataChunk, len(missing))]
		var metas []struct {
			ID       int
			Metadata string
		}
		if err := sched.WithContext(ctx).Table("acquiredimage").Select(`"Id" AS id, metadata`).Where(`"Id" IN ?`, ids).Scan(&metas).Error; err != nil {
			return nil, fmt.Errorf("load acquired image metadata: %w", err)
		}
		for _, m := range metas {
			l.files[m.ID] = FileBase(m.Metadata)
		}
	}
	out := map[string][]Target{}
	for _, r := range rows {
		f := l.files[r.ID]
		if f == "" || r.TargetID == nil {
			continue
		}
		if t, ok := byID[*r.TargetID]; ok {
			out[f] = append(out[f], t)
		}
	}
	return out, nil
}

func FileBase(metadata string) string {
	var md struct {
		FileName string `json:"FileName"`
	}
	if json.Unmarshal([]byte(metadata), &md) != nil {
		return ""
	}
	return md.FileName[strings.LastIndexAny(md.FileName, `\/`)+1:]
}

func ObjectTargets(ctx context.Context, appDB *gorm.DB, targets []Target) (map[string]string, error) {
	var rows []struct {
		Object     string
		TargetGUID string
		N          int
	}
	if err := appDB.WithContext(ctx).Model(&app.FrameTarget{}).
		Select("object, target_guid, count(*) AS n").Group("object, target_guid").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load frame links: %w", err)
	}
	best := map[string]int{}
	out := map[string]string{}
	for _, r := range rows {
		if r.Object == "" {
			continue
		}
		if r.N > best[r.Object] || (r.N == best[r.Object] && r.TargetGUID < out[r.Object]) {
			best[r.Object], out[r.Object] = r.N, r.TargetGUID
		}
	}
	nameCount := map[string]int{}
	for _, t := range targets {
		nameCount[t.Name]++
	}
	for _, t := range targets {
		if _, ok := out[t.Name]; !ok && t.GUID != "" && nameCount[t.Name] == 1 {
			out[t.Name] = t.GUID
		}
	}
	return out, nil
}

func TargetObjects(objectTargets map[string]string) map[string][]string {
	out := map[string][]string{}
	for obj, guid := range objectTargets {
		out[guid] = append(out[guid], obj)
	}
	for _, objs := range out {
		slices.Sort(objs)
	}
	return out
}
