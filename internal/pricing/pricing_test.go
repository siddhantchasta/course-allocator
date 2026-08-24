package pricing

import "testing"

func TestCalculate(t *testing.T) {
	cases := []struct {
		name           string
		availableSeats int
		want           int
	}{
		{"full inventory", 100, BaseCredits},
		{"exactly at threshold", 50, BaseCredits},
		{"one seat past threshold, rounds down to no surge yet", 49, BaseCredits},
		{"ten seats past threshold adds one credit step", 40, BaseCredits + 1},
		{"deep scarcity climbs further", 5, BaseCredits + 4},
		{"cap only actually triggers once seats hit zero", 0, MaxCredits},
		{"negative input is guarded, not just theoretical", -3, MaxCredits},
		{"one seat left still lands under the cap", 1, BaseCredits + 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Calculate(tc.availableSeats)
			if got != tc.want {
				t.Errorf("Calculate(%d) = %d, want %d", tc.availableSeats, got, tc.want)
			}
		})
	}
}
