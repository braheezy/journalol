package replay

import "testing"

func TestSpectatorFocusKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		participant int
		team        int
		code        uint16
		label       string
	}{
		{1, 100, 18, "1"},
		{2, 100, 19, "2"},
		{3, 100, 20, "3"},
		{4, 100, 21, "4"},
		{5, 100, 23, "5"},
		{6, 200, 12, "Q"},
		{7, 200, 13, "W"},
		{8, 200, 14, "E"},
		{9, 200, 15, "R"},
		{10, 200, 17, "T"},
	}
	for _, test := range tests {
		code, label, err := SpectatorFocusKey(test.participant, test.team)
		if err != nil {
			t.Fatalf("SpectatorFocusKey(%d, %d): %v", test.participant, test.team, err)
		}
		if code != test.code || label != test.label {
			t.Fatalf("SpectatorFocusKey(%d, %d) = %d/%q, want %d/%q",
				test.participant, test.team, code, label, test.code, test.label)
		}
	}
}

func TestSpectatorFocusKeyRejectsInconsistentTeam(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		participant int
		team        int
	}{{0, 100}, {11, 200}, {5, 200}, {6, 100}} {
		if _, _, err := SpectatorFocusKey(test.participant, test.team); err == nil {
			t.Fatalf("SpectatorFocusKey(%d, %d) unexpectedly succeeded", test.participant, test.team)
		}
	}
}
