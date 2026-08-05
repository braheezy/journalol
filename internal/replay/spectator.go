package replay

import "fmt"

// SpectatorFocusKey returns the macOS virtual key used by League's replay
// spectator controls for one participant. Summoner's Rift participants 1-5
// are blue-side keys 1-5; participants 6-10 are red-side keys Q-W-E-R-T.
// Team validation prevents a malformed import from focusing another player.
func SpectatorFocusKey(participantID, teamID int) (uint16, string, error) {
	type key struct {
		code  uint16
		label string
		team  int
	}
	keys := [...]key{
		{},
		{code: 18, label: "1", team: 100},
		{code: 19, label: "2", team: 100},
		{code: 20, label: "3", team: 100},
		{code: 21, label: "4", team: 100},
		{code: 23, label: "5", team: 100},
		{code: 12, label: "Q", team: 200},
		{code: 13, label: "W", team: 200},
		{code: 14, label: "E", team: 200},
		{code: 15, label: "R", team: 200},
		{code: 17, label: "T", team: 200},
	}
	if participantID < 1 || participantID >= len(keys) {
		return 0, "", fmt.Errorf("replay participant ID %d is outside 1-10", participantID)
	}
	selected := keys[participantID]
	if selected.team != teamID {
		return 0, "", fmt.Errorf("replay participant %d belongs to team %d, not team %d", participantID, selected.team, teamID)
	}
	return selected.code, selected.label, nil
}
