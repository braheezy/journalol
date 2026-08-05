package riot

import (
	"errors"
	"testing"
)

func TestParseRegionalRoute(t *testing.T) {
	t.Parallel()

	route, err := ParseRegionalRoute(" europe ")
	if err != nil {
		t.Fatalf("ParseRegionalRoute: %v", err)
	}
	if route != RouteEurope {
		t.Fatalf("route = %q, want %q", route, RouteEurope)
	}

	if _, err := ParseRegionalRoute("moon"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid route error = %v, want ErrInvalidArgument", err)
	}
}

func TestRegionalRouteForPlatform(t *testing.T) {
	t.Parallel()

	tests := map[string]RegionalRoute{
		"NA1":  RouteAmericas,
		"br1":  RouteAmericas,
		"EUW1": RouteEurope,
		"TR1":  RouteEurope,
		"KR":   RouteAsia,
		"JP1":  RouteAsia,
		"OC1":  RouteSEA,
		"VN2":  RouteSEA,
	}
	for platform, want := range tests {
		platform, want := platform, want
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			got, err := RegionalRouteForPlatform(platform)
			if err != nil {
				t.Fatalf("RegionalRouteForPlatform: %v", err)
			}
			if got != want {
				t.Fatalf("route = %q, want %q", got, want)
			}
		})
	}

	if _, err := RegionalRouteForPlatform("XYZ"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid platform error = %v, want ErrInvalidArgument", err)
	}
}

func TestRegionalBaseURL(t *testing.T) {
	t.Parallel()

	got, err := RouteAmericas.BaseURL()
	if err != nil {
		t.Fatalf("BaseURL: %v", err)
	}
	if got != "https://americas.api.riotgames.com" {
		t.Fatalf("BaseURL = %q", got)
	}
	if _, err := RegionalRoute("INVALID").BaseURL(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid route error = %v, want ErrInvalidArgument", err)
	}
}
