package schedcmd_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

const (
	jTrue      = "true"
	jFalse     = "false"
	labelFlats = "Flats"
	wantOff    = "Off"
)

func createWith(fields string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"project":{"guid":"pg","name":"Gecko Nebula","priority":1,"state":1,"minimumtime":60,"minimumaltitude":15%s},"targets":[{"guid":"tg","name":"Gecko Nebula","ra_hours":22.53,"dec":40.83,"rotation":0,"plans":[{"guid":"pp","template_id":3,"template_name":"H-a","exposure":600,"desired":300}]}]}`, fields))
}

func TestProjectCreateSettingsValidation(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectCreate)
	good := []string{
		"",
		`,"ditherevery":0,"filterswitchfrequency":999,"flatshandling":0`,
		`,"flatshandling":99`,
		`,"flatshandling":100`,
		`,"flatshandling":200`,
		`,"meridianwindow":720,"horizonoffset":90,"maximumaltitude":0`,
		`,"maximumaltitude":60,"smartexposureorder":true,"usecustomhorizon":false,"enablegrader":true`,
	}
	for _, g := range good {
		if err := s.Validate(createWith(g)); err != nil {
			t.Fatalf("rejected %q: %v", g, err)
		}
	}
	bad := []string{
		`,"ditherevery":-1`,
		`,"ditherevery":1000`,
		`,"filterswitchfrequency":1000`,
		`,"flatshandling":150`,
		`,"flatshandling":201`,
		`,"flatshandling":-1`,
		`,"meridianwindow":721`,
		`,"horizonoffset":-1`,
		`,"horizonoffset":91`,
		`,"maximumaltitude":91`,
		`,"maximumaltitude":15`,
		`,"maximumaltitude":10`,
		`,"smartexposureorder":1`,
		`,"ditherevery":"2"`,
		`,"ditherevery":1.5`,
	}
	for _, b := range bad {
		if err := s.Validate(createWith(b)); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestProjectCreateNoteNamesSettings(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectCreate)
	d, err := s.Describe(createWith(`,"ditherevery":2,"filterswitchfrequency":1,"flatshandling":1`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Note, "Dither every 2 · filter switch every 1 · flats every 1 day.") {
		t.Fatalf("%q", d.Note)
	}
	d, _ = s.Describe(createWith(`,"flatshandling":100`))
	if d.Note != "Flats target completion." {
		t.Fatalf("%q", d.Note)
	}
	d, _ = s.Describe(createWith(""))
	if d.Note != "" {
		t.Fatalf("%q", d.Note)
	}
}

func TestProjectCreateKeepsSettingsKeys(t *testing.T) {
	t.Parallel()
	two, on := 2, true
	alt := 70.0
	b, err := json.Marshal(schedcmd.NewProject{GUID: "g", Name: "n", ProjectSettings: schedcmd.ProjectSettings{DitherEvery: &two, EnableGrader: &on, MaximumAltitude: &alt}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"ditherevery":2`, `"enablegrader":true`, `"maximumaltitude":70`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s lacks %s", got, want)
		}
	}
	if strings.Contains(got, "flatshandling") {
		t.Fatalf("unset fields should be left out: %s", got)
	}
}

func projectEdit(field, before, after string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":5,"name":"Cygnis Loop","changes":[{"field":%q,"before":%s,"after":%s}]}`, field, before, after))
}

func TestProjectEditSettingsValidation(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectEdit)
	good := [][3]string{
		{schedcmd.FieldDitherEvery, "0", "2"},
		{schedcmd.FieldFilterSwitchFrequency, "0", "1"},
		{schedcmd.FieldFlatsHandling, "0", "100"},
		{schedcmd.FieldFlatsHandling, "0", "200"},
		{schedcmd.FieldFlatsHandling, "0", "7"},
		{schedcmd.FieldSmartExposureOrder, jFalse, jTrue},
		{schedcmd.FieldSmartExposureOrder, "0", jTrue},
		{schedcmd.FieldMeridianWindow, "0", "60"},
		{schedcmd.FieldUseCustomHorizon, jTrue, jFalse},
		{schedcmd.FieldHorizonOffset, "0", "2.5"},
		{schedcmd.FieldMaximumAltitude, "0", "75"},
		{schedcmd.FieldEnableGrader, jTrue, jFalse},
		{"priority", "0", "2"},
	}
	for _, g := range good {
		if err := s.Validate(projectEdit(g[0], g[1], g[2])); err != nil {
			t.Fatalf("rejected %v: %v", g, err)
		}
	}
	bad := [][3]string{
		{schedcmd.FieldDitherEvery, "0", "1000"},
		{schedcmd.FieldDitherEvery, "0", "-1"},
		{schedcmd.FieldDitherEvery, "0", `"2"`},
		{schedcmd.FieldDitherEvery, "0", "null"},
		{schedcmd.FieldFilterSwitchFrequency, "0", "1.5"},
		{schedcmd.FieldFlatsHandling, "0", "150"},
		{schedcmd.FieldSmartExposureOrder, jFalse, "1"},
		{schedcmd.FieldUseCustomHorizon, jFalse, `"true"`},
		{schedcmd.FieldMeridianWindow, "0", "721"},
		{schedcmd.FieldHorizonOffset, "0", "91"},
		{schedcmd.FieldMaximumAltitude, "0", "-5"},
		{schedcmd.FieldEnableGrader, jTrue, "0"},
	}
	for _, b := range bad {
		if err := s.Validate(projectEdit(b[0], b[1], b[2])); err == nil {
			t.Fatalf("accepted %v", b)
		}
	}
}

func TestProjectEditSettingsFormats(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectEdit)
	cases := []struct {
		field, before, after, label, wantBefore, wantAfter string
	}{
		{schedcmd.FieldFlatsHandling, "0", "100", labelFlats, wantOff, "Target completion"},
		{schedcmd.FieldFlatsHandling, "200", "3", labelFlats, "Use with immediate", "Every 3 days"},
		{schedcmd.FieldFlatsHandling, "2", "1", labelFlats, "Every 2 days", "Every 1 day"},
		{schedcmd.FieldMeridianWindow, "0", "60", "Meridian window", "0 min", "60 min"},
		{schedcmd.FieldMaximumAltitude, "0", "75", "Maximum altitude", wantOff, "75°"},
		{schedcmd.FieldHorizonOffset, "0", "2.5", "Horizon offset", "0°", "2.5°"},
		{schedcmd.FieldSmartExposureOrder, jFalse, jTrue, "Smart exposure order", wantOff, "On"},
		{schedcmd.FieldUseCustomHorizon, "1", jFalse, "Custom horizon", "On", wantOff},
		{schedcmd.FieldEnableGrader, jTrue, jFalse, "Grader", "On", wantOff},
		{schedcmd.FieldDitherEvery, "0", "2", "Dither every", "0", "2"},
		{schedcmd.FieldFilterSwitchFrequency, "0", "1", "Filter switch frequency", "0", "1"},
	}
	for _, c := range cases {
		d, err := s.Describe(projectEdit(c.field, c.before, c.after))
		if err != nil {
			t.Fatal(err)
		}
		df := d.Diffs[0]
		var before, after string
		if json.Unmarshal(df.Before, &before) != nil {
			before = string(df.Before)
		}
		if json.Unmarshal(df.After, &after) != nil {
			after = string(df.After)
		}
		if df.Field != c.label || before != c.wantBefore || after != c.wantAfter {
			t.Fatalf("%s: got %q %q -> %q", c.field, df.Field, before, after)
		}
	}
}

func TestProjectBatchEditChecksSettings(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectBatchEdit)
	ok := `{"items":[{"id":5,"name":"A","changes":[{"field":"ditherevery","before":0,"after":2}]},{"id":7,"name":"B","changes":[{"field":"flatshandling","before":0,"after":1}]}]}`
	if err := s.Validate(json.RawMessage(ok)); err != nil {
		t.Fatal(err)
	}
	bad := `{"items":[{"id":5,"name":"A","changes":[{"field":"ditherevery","before":0,"after":2}]},{"id":7,"name":"B","changes":[{"field":"flatshandling","before":0,"after":150}]}]}`
	if err := s.Validate(json.RawMessage(bad)); err == nil {
		t.Fatal("accepted flats 150 in a batch")
	}
}
