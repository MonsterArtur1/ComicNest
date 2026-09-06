package library

import "testing"

func TestParseFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Parsed
	}{
		{
			name: "hash number",
			in:   "Batman #012",
			want: Parsed{Series: "Batman", Number: "012"},
		},
		{
			name: "hash number with year",
			in:   "Batman #12 (2020)",
			want: Parsed{Series: "Batman", Number: "12", Year: "2020"},
		},
		{
			name: "number with year and extra groups plus extension",
			in:   "Saga 055 (2020) (Digital) (Empire).cbz",
			want: Parsed{Series: "Saga", Number: "055", Year: "2020"},
		},
		{
			name: "volume marker stays in series",
			in:   "Monstress v2 015",
			want: Parsed{Series: "Monstress v2", Number: "015"},
		},
		{
			name: "no pattern falls back to title",
			in:   "One-Shot Special",
			want: Parsed{Title: "One-Shot Special"},
		},
		{
			name: "one-shot fallback strips paren groups and keeps year",
			in:   "The Man Who Dreamt the Impossible - A Tribute to Jack Kirby, Treasury Edition (2025) (Digital) (Zone-Empire).cbr",
			want: Parsed{Title: "The Man Who Dreamt the Impossible - A Tribute to Jack Kirby, Treasury Edition", Year: "2025"},
		},
		{
			name: "plain number with year",
			in:   "Y The Last Man 60 (2008)",
			want: Parsed{Series: "Y The Last Man", Number: "60", Year: "2008"},
		},
		{
			name: "underscores become spaces",
			in:   "Batman_012_(2020)",
			want: Parsed{Series: "Batman", Number: "012", Year: "2020"},
		},
		{
			name: "underscores with hash",
			in:   "Batman_#012",
			want: Parsed{Series: "Batman", Number: "012"},
		},
		{
			name: "decimal issue number",
			in:   "Saga 12.1",
			want: Parsed{Series: "Saga", Number: "12.1"},
		},
		{
			name: "extension stripped without extra groups",
			in:   "Hellboy 001.cbr",
			want: Parsed{Series: "Hellboy", Number: "001"},
		},
		{
			name: "pdf extension stripped",
			in:   "Some Comic 003.pdf",
			want: Parsed{Series: "Some Comic", Number: "003"},
		},
		{
			name: "trailing dash before number removed from series",
			in:   "Cool Series - 004",
			want: Parsed{Series: "Cool Series", Number: "004"},
		},
		{
			name: "no number anywhere",
			in:   "Just A Title",
			want: Parsed{Title: "Just A Title"},
		},
		{
			name: "collapses double spaces",
			in:   "Weird   Spacing   007",
			want: Parsed{Series: "Weird Spacing", Number: "007"},
		},
		{
			name: "subtitle ending in a digit is not mistaken for the issue number",
			in:   "The Boys 52 - Barbary Coast 1 (2011) (HD) (digital-Empire).cbz",
			want: Parsed{Series: "The Boys", Number: "52", Title: "Barbary Coast 1", Year: "2011"},
		},
		{
			name: "worded subtitle after the issue number is kept as title",
			in:   "The Boys 052 - Some Arc Name",
			want: Parsed{Series: "The Boys", Number: "052", Title: "Some Arc Name"},
		},
		{
			name: "leading number that looks like a year is skipped for a later one",
			in:   "2000 AD 1957 (2015).cbr",
			want: Parsed{Series: "2000 AD", Number: "1957", Year: "2015"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFilename(tc.in)
			if got != tc.want {
				t.Errorf("ParseFilename(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"page2.jpg", "page10.jpg", true},
		{"page10.jpg", "page2.jpg", false},
		{"PAGE2.jpg", "page10.jpg", true},
		{"page2.jpg", "PAGE2.JPG", false}, // equal case-insensitively
		{"PAGE2.JPG", "page2.jpg", false},
		{"a", "a", false},
		{"a", "b", true},
		{"b", "a", false},
		{"b/page2.png", "cover.jpg", true},
		{"cover.jpg", "b/page10.png", false},
		{"page1.jpg", "page1.jpg", false},
		{"page09.jpg", "page9.jpg", false}, // equal numeric value, neither strictly less
	}

	for _, tc := range cases {
		got := NaturalLess(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("NaturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
