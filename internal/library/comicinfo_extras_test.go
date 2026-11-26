package library

import (
	"reflect"
	"testing"
)

func TestParseComicInfoCharactersAndArcs(t *testing.T) {
	info, err := parseComicInfo([]byte(`<ComicInfo>
  <Series>Event</Series>
  <Characters> Spider-Man, Venom ;spider-man,, Carnage </Characters>
  <StoryArc>Maximum Carnage, Web of Shadows</StoryArc>
  <StoryArcNumber>3, 7</StoryArcNumber>
</ComicInfo>`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.CharacterList(), []string{"Spider-Man", "Venom", "Carnage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CharacterList = %q, want %q", got, want)
	}
	want := []StoryArcRef{{"Maximum Carnage", 3}, {"Web of Shadows", 7}}
	if got := info.StoryArcs(); !reflect.DeepEqual(got, want) {
		t.Errorf("StoryArcs = %+v, want %+v", got, want)
	}
}

func TestComicInfoStoryArcNumbers(t *testing.T) {
	cases := []struct {
		arc, num string
		want     []StoryArcRef
	}{
		{"Civil War", "", []StoryArcRef{{"Civil War", 0}}},
		{"Civil War", "5", []StoryArcRef{{"Civil War", 5}}},
		{"Civil War", "x", []StoryArcRef{{"Civil War", 0}}},
		{"A, B", "2", []StoryArcRef{{"A", 2}, {"B", 0}}}, // numbers pair by index
		{"A; a", "1;2", []StoryArcRef{{"A", 1}}},         // duplicate names collapse
		{"  ", "1", []StoryArcRef{}},
	}
	for _, tc := range cases {
		ci := &ComicInfo{StoryArc: tc.arc, StoryArcNumber: tc.num}
		if got := ci.StoryArcs(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("StoryArcs(%q, %q) = %+v, want %+v", tc.arc, tc.num, got, tc.want)
		}
	}
	if got := (&ComicInfo{}).CharacterList(); len(got) != 0 {
		t.Errorf("CharacterList of empty = %q", got)
	}
}
