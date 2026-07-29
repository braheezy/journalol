package web

import "testing"

func TestFormatDecimal(t *testing.T) {
	tests := []struct {
		value  float64
		places int
		want   string
	}{
		{value: 0, places: 0, want: "0"},
		{value: 10, places: 0, want: "10"},
		{value: 10, places: 1, want: "10"},
		{value: 5.25, places: 2, want: "5.25"},
	}

	for _, test := range tests {
		if got := formatDecimal(test.value, test.places); got != test.want {
			t.Errorf("formatDecimal(%v, %d) = %q, want %q", test.value, test.places, got, test.want)
		}
	}
}
