package riot

import (
	"fmt"
	"strings"
)

// RegionalRoute identifies a Riot regional routing cluster used by ACCOUNT-V1
// and MATCH-V5. It is deliberately distinct from a League platform route such
// as NA1 or EUW1.
type RegionalRoute string

const (
	RouteAmericas RegionalRoute = "AMERICAS"
	RouteAsia     RegionalRoute = "ASIA"
	RouteEurope   RegionalRoute = "EUROPE"
	RouteSEA      RegionalRoute = "SEA"
)

var regionalBaseURLs = map[RegionalRoute]string{
	RouteAmericas: "https://americas.api.riotgames.com",
	RouteAsia:     "https://asia.api.riotgames.com",
	RouteEurope:   "https://europe.api.riotgames.com",
	RouteSEA:      "https://sea.api.riotgames.com",
}

var platformRegionalRoutes = map[string]RegionalRoute{
	"BR1":  RouteAmericas,
	"LA1":  RouteAmericas,
	"LA2":  RouteAmericas,
	"NA1":  RouteAmericas,
	"EUN1": RouteEurope,
	"EUW1": RouteEurope,
	"RU":   RouteEurope,
	"TR1":  RouteEurope,
	"JP1":  RouteAsia,
	"KR":   RouteAsia,
	"OC1":  RouteSEA,
	"PH2":  RouteSEA,
	"SG2":  RouteSEA,
	"TH2":  RouteSEA,
	"TW2":  RouteSEA,
	"VN2":  RouteSEA,
}

// ParseRegionalRoute validates and canonicalizes a regional route name.
func ParseRegionalRoute(value string) (RegionalRoute, error) {
	route := RegionalRoute(strings.ToUpper(strings.TrimSpace(value)))
	if _, ok := regionalBaseURLs[route]; !ok {
		return "", fmt.Errorf("%w: unsupported regional route", ErrInvalidArgument)
	}
	return route, nil
}

// RegionalRouteForPlatform returns the regional cluster used by ACCOUNT-V1
// and MATCH-V5 for a League platform route.
func RegionalRouteForPlatform(platform string) (RegionalRoute, error) {
	route, ok := platformRegionalRoutes[strings.ToUpper(strings.TrimSpace(platform))]
	if !ok {
		return "", fmt.Errorf("%w: unsupported platform route", ErrInvalidArgument)
	}
	return route, nil
}

// BaseURL returns Riot's public base URL for the regional route.
func (r RegionalRoute) BaseURL() (string, error) {
	baseURL, ok := regionalBaseURLs[r]
	if !ok {
		return "", fmt.Errorf("%w: unsupported regional route", ErrInvalidArgument)
	}
	return baseURL, nil
}
