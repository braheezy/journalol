package metrics

import "testing"

func TestKDA(t *testing.T) {
	tests := []struct {
		name                   string
		kills, deaths, assists int
		want                   float64
	}{
		{name: "normal", kills: 4, deaths: 2, assists: 6, want: 5},
		{name: "zero deaths", kills: 3, deaths: 0, assists: 7, want: 10},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := KDA(test.kills, test.deaths, test.assists); got != test.want {
				t.Fatalf("KDA() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestVisionPerMinute(t *testing.T) {
	got, ok := VisionPerMinute(30, 1800)
	if !ok {
		t.Fatal("VisionPerMinute() unavailable, want available")
	}
	if got != 1 {
		t.Fatalf("VisionPerMinute() = %v, want 1", got)
	}

	if _, ok := VisionPerMinute(20, 0); ok {
		t.Fatal("VisionPerMinute() available for zero duration")
	}
}

func TestMeets(t *testing.T) {
	got, err := Meets("<=", 4, 5)
	if err != nil {
		t.Fatalf("Meets() error = %v", err)
	}
	if !got {
		t.Fatal("Meets() = false, want true")
	}

	if _, err := Meets("LIKE", 4, 5); err == nil {
		t.Fatal("Meets() error = nil for unsupported comparator")
	}
}
