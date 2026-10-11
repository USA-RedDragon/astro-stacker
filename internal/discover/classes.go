package discover

import "context"

func (s *Service) ProjectClasses(ctx context.Context) (map[int]string, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return projectClasses(snap), nil
}

func projectClasses(snap *snapshot) map[int]string {
	out := map[int]string{}
	for _, subj := range snap.subjects {
		if subj.Kind != SubjectProject || subj.ProjectID <= 0 {
			continue
		}
		for _, l := range snap.bySubj[subj.Key] {
			if !l.Linked() {
				continue
			}
			if class, _ := PickClass(l.Object.Type); class != "" {
				out[subj.ProjectID] = class
			}
			break
		}
	}
	return out
}
