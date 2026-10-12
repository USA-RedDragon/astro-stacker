package schedcmd_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

const notDry = `{"dry_run":false}`

func TestTemplatesMigrateKinds(t *testing.T) {
	t.Parallel()
	for _, k := range []schedcmd.Kind{schedcmd.KindTemplatesMigrate, schedcmd.KindTemplatesRevert} {
		s := spec(t, k)
		if s.Category() != schedcmd.CategoryTemplates || s.Destination() != schedcmd.DestinationObservatory {
			t.Errorf("%s: %s %s", k, s.Category(), s.Destination())
		}
		for _, bad := range []string{``, `{}`, ` null `, `{"dry_run":"yes"}`, `[]`} {
			if err := s.Validate(json.RawMessage(bad)); !errors.Is(err, schedcmd.ErrInvalid) {
				t.Errorf("%s accepted %q: %v", k, bad, err)
			}
		}
		d, err := s.Describe(json.RawMessage(`{"dry_run":true}`))
		if err != nil || d.Note != "Dry run: reports what would change and writes nothing." || len(d.Diffs) != 1 {
			t.Errorf("%s dry run %+v %v", k, d, err)
		}
		if _, _, err := s.Inverse(json.RawMessage(`{"dry_run":true}`)); !errors.Is(err, schedcmd.ErrNoInverse) {
			t.Errorf("%s: a dry run has an undo: %v", k, err)
		}
	}
	d, err := spec(t, schedcmd.KindTemplatesMigrate).Describe(json.RawMessage(notDry))
	if err != nil || d.Title != "Move every plan to one template per filter" {
		t.Fatalf("%+v %v", d, err)
	}
	k, inv, err := spec(t, schedcmd.KindTemplatesMigrate).Inverse(json.RawMessage(notDry))
	if err != nil || k != schedcmd.KindTemplatesRevert || string(inv) != notDry {
		t.Fatalf("undo %s %s %v", k, inv, err)
	}
	k, _, err = spec(t, schedcmd.KindTemplatesRevert).Inverse(json.RawMessage(notDry))
	if err != nil || k != schedcmd.KindTemplatesMigrate {
		t.Fatalf("undo of revert %s %v", k, err)
	}
}
