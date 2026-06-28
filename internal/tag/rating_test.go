package tag

import "testing"

func TestFmpsToStars(t *testing.T) {
	cases := []struct {
		in    string
		want  int
		wantK bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"0.0", 0, false},
		{"nope", 0, false},
		{"0.2", 1, true},
		{"0.4", 2, true},
		{"0.6", 3, true},
		{"0.8", 4, true},
		{"1.0", 5, true},
		{"0.01", 1, true},
		{"1.5", 5, true}, // clamped
	}
	for _, c := range cases {
		got, ok := fmpsToStars(c.in)
		if got != c.want || ok != c.wantK {
			t.Errorf("fmpsToStars(%q) = (%d, %t), want (%d, %t)", c.in, got, ok, c.want, c.wantK)
		}
	}
}

func TestRatingIntToStars(t *testing.T) {
	cases := []struct {
		in    string
		want  int
		wantK bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"6", 0, false},
		{"x", 0, false},
		{"1", 1, true},
		{"5", 5, true},
	}
	for _, c := range cases {
		got, ok := ratingIntToStars(c.in)
		if got != c.want || ok != c.wantK {
			t.Errorf("ratingIntToStars(%q) = (%d, %t), want (%d, %t)", c.in, got, ok, c.want, c.wantK)
		}
	}
}

func TestPopmScales(t *testing.T) {
	wmp := map[byte]int{0: 0, 1: 1, 64: 2, 128: 3, 196: 4, 255: 5, 200: 5}
	for b, want := range wmp {
		if got := popmWMPToStars(b); got != want {
			t.Errorf("popmWMPToStars(%d) = %d, want %d", b, got, want)
		}
	}
	itunes := map[byte]int{0: 0, 20: 1, 40: 2, 60: 3, 80: 4, 100: 5, 255: 5}
	for b, want := range itunes {
		if got := popmITunesToStars(b); got != want {
			t.Errorf("popmITunesToStars(%d) = %d, want %d", b, got, want)
		}
	}
}

func TestRatingCandidatesResolveOrder(t *testing.T) {
	t.Cleanup(func() { ConfigureRatingOrder(nil) })

	rc := ratingCandidates{mediaMonkey: 1, foobar: 2, wmp: 3, itunes: 4}

	ConfigureRatingOrder([]string{SourceWMP, SourceITunes, SourceMediaMonkey, SourceFoobar2000})
	if got := rc.resolve(); got != 3 {
		t.Fatalf("resolve() with WMP first = %d, want 3", got)
	}

	ConfigureRatingOrder([]string{SourceMediaMonkey, SourceFoobar2000})
	if got := rc.resolve(); got != 1 {
		t.Fatalf("resolve() with MediaMonkey first = %d, want 1", got)
	}

	// Order naming only the absent source falls through to nothing.
	ConfigureRatingOrder([]string{SourceMediaMonkey})
	if got := (ratingCandidates{foobar: 2}).resolve(); got != 0 {
		t.Fatalf("resolve() with only-absent source = %d, want 0", got)
	}
}

func TestConfigureRatingOrderDefaultsAndFilters(t *testing.T) {
	t.Cleanup(func() { ConfigureRatingOrder(nil) })

	ConfigureRatingOrder(nil)
	if len(ratingTagOrder) != len(DefaultRatingTagOrder) {
		t.Fatalf("nil order did not reset to default, got %v", ratingTagOrder)
	}

	// Unknown sources are dropped; an all-unknown order falls back to default.
	ConfigureRatingOrder([]string{"bogus"})
	if len(ratingTagOrder) != len(DefaultRatingTagOrder) {
		t.Fatalf("all-unknown order did not fall back to default, got %v", ratingTagOrder)
	}

	ConfigureRatingOrder([]string{"bogus", SourceFoobar2000})
	if len(ratingTagOrder) != 1 || ratingTagOrder[0] != SourceFoobar2000 {
		t.Fatalf("filtered order = %v, want [foobar2000]", ratingTagOrder)
	}
}
