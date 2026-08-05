// Package importer turns Riot API documents into Journalol's stable import
// model and coordinates their durable persistence.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"journalol/internal/riot"
)

const NormalizerVersion = 1

var (
	ErrInvalidMatch    = errors.New("invalid Riot match")
	ErrPlayerNotInGame = errors.New("configured player is not present in Riot match")
)

// NormalizedMatch is independent of both Riot's transport DTO and SQLite. It
// makes normalization rules directly testable and keeps API/schema changes
// from leaking through the application.
type NormalizedMatch struct {
	RiotMatchID       string
	QueueID           int
	QueueType         string
	MapID             int
	GameMode          string
	GameType          string
	Patch             string
	StartedAt         time.Time
	EndedAt           time.Time
	DurationSeconds   int
	IsRemake          bool
	Surrendered       bool
	Completeness      string
	NormalizerVersion int
	Stats             NormalizedPlayerStats
	Events            []NormalizedTimelineEvent
}

type NormalizedPlayerStats struct {
	ParticipantID     int
	TeamID            int
	ChampionID        int
	ChampionName      string
	Role              string
	Win               bool
	Kills             int
	Deaths            int
	Assists           int
	LaneMinions       int
	NeutralMinions    int
	Gold              int
	ChampionDamage    int
	VisionScore       int
	WardsPlaced       int
	WardsKilled       int
	VisionWardsBought int
	OpponentChampion  string
	FinalItems        []int
	Runes             []int
	SummonerSpells    []int
	SkillOrder        []int
}

type NormalizedTimelineEvent struct {
	SequenceNumber      int
	TimestampMS         int64
	EventType           string
	ActorParticipantID  *int
	VictimParticipantID *int
	TeamID              *int
	PositionX           *int
	PositionY           *int
	DataJSON            string
}

// NormalizeMatch applies the first versioned set of Journalol match rules.
func NormalizeMatch(detail riot.Match, puuid string) (NormalizedMatch, error) {
	matchID := strings.TrimSpace(detail.Metadata.MatchID)
	puuid = strings.TrimSpace(puuid)
	if matchID == "" {
		return NormalizedMatch{}, fmt.Errorf("%w: match ID is missing", ErrInvalidMatch)
	}
	if puuid == "" {
		return NormalizedMatch{}, fmt.Errorf("%w: player PUUID is missing", ErrInvalidMatch)
	}

	participant, ok := participantByPUUID(detail.Info.Participants, puuid)
	if !ok {
		return NormalizedMatch{}, fmt.Errorf("%w: PUUID not found in participants", ErrPlayerNotInGame)
	}
	if participant.ParticipantID < 1 {
		return NormalizedMatch{}, fmt.Errorf("%w: participant ID is missing", ErrInvalidMatch)
	}

	duration := normalizedDuration(detail.Info.GameDuration, participant.TimePlayed)
	if duration < 0 {
		return NormalizedMatch{}, fmt.Errorf("%w: game duration is negative", ErrInvalidMatch)
	}
	startMillis := detail.Info.GameStartTimestamp
	if startMillis == 0 {
		startMillis = detail.Info.GameCreation
	}
	if startMillis <= 0 {
		return NormalizedMatch{}, fmt.Errorf("%w: game start time is missing", ErrInvalidMatch)
	}
	startedAt := time.UnixMilli(startMillis).UTC()
	endedAt := time.Time{}
	if detail.Info.GameEndTimestamp > 0 {
		endedAt = time.UnixMilli(detail.Info.GameEndTimestamp).UTC()
	}
	if endedAt.IsZero() && duration > 0 {
		endedAt = startedAt.Add(time.Duration(duration) * time.Second)
	}
	if duration == 0 && !endedAt.IsZero() {
		duration = int(endedAt.Sub(startedAt).Round(time.Second) / time.Second)
	}
	if endedAt.IsZero() {
		endedAt = startedAt
	}
	if duration < 0 || endedAt.Before(startedAt) {
		return NormalizedMatch{}, fmt.Errorf("%w: game end precedes game start", ErrInvalidMatch)
	}
	if err := validateParticipantNumbers(participant); err != nil {
		return NormalizedMatch{}, err
	}

	role := participantRole(participant)
	explicitRemake := participant.GameEndedInEarlySurrender ||
		strings.Contains(strings.ToLower(detail.Info.EndOfGameResult), "earlysurrender")
	// Older payloads did not consistently expose the explicit early-surrender
	// flags. Even explicit signals are bounded so a later ordinary surrender is
	// never excluded from review as a remake.
	isRemake := (explicitRemake && (duration == 0 || duration < 10*60)) ||
		(detail.Info.MapID == 11 && duration > 0 && duration < 5*60)

	stats := NormalizedPlayerStats{
		ParticipantID:     participant.ParticipantID,
		TeamID:            participant.TeamID,
		ChampionID:        participant.ChampionID,
		ChampionName:      strings.TrimSpace(participant.ChampionName),
		Role:              role,
		Win:               participant.Win,
		Kills:             participant.Kills,
		Deaths:            participant.Deaths,
		Assists:           participant.Assists,
		LaneMinions:       participant.TotalMinionsKilled,
		NeutralMinions:    participant.NeutralMinionsKilled,
		Gold:              participant.GoldEarned,
		ChampionDamage:    participant.TotalDamageDealtToChampions,
		VisionScore:       participant.VisionScore,
		WardsPlaced:       participant.WardsPlaced,
		WardsKilled:       participant.WardsKilled,
		VisionWardsBought: participant.VisionWardsBoughtInGame,
		OpponentChampion:  opponentChampion(detail.Info.Participants, participant, role),
		FinalItems: compactPositive([]int{
			participant.Item0,
			participant.Item1,
			participant.Item2,
			participant.Item3,
			participant.Item4,
			participant.Item5,
			participant.Item6,
		}),
		Runes:          participantRunes(participant.Perks),
		SummonerSpells: compactPositive([]int{participant.Summoner1ID, participant.Summoner2ID}),
		SkillOrder:     []int{},
	}

	return NormalizedMatch{
		RiotMatchID:       matchID,
		QueueID:           detail.Info.QueueID,
		QueueType:         queueLabel(detail.Info.QueueID),
		MapID:             detail.Info.MapID,
		GameMode:          strings.TrimSpace(detail.Info.GameMode),
		GameType:          strings.TrimSpace(detail.Info.GameType),
		Patch:             patchLabel(detail.Info.GameVersion),
		StartedAt:         startedAt,
		EndedAt:           endedAt,
		DurationSeconds:   duration,
		IsRemake:          isRemake,
		Surrendered:       (participant.GameEndedInSurrender || explicitRemake) && !isRemake,
		Completeness:      "detail_only",
		NormalizerVersion: NormalizerVersion,
		Stats:             stats,
		Events:            []NormalizedTimelineEvent{},
	}, nil
}

// ApplyTimeline validates and attaches Journalol's selected timeline events.
// Sequence numbers count every source event, including ignored types, so a
// future change to the selected set does not silently renumber old events.
func ApplyTimeline(match *NormalizedMatch, timeline riot.Timeline) error {
	if match == nil {
		return fmt.Errorf("%w: normalized match is nil", ErrInvalidMatch)
	}
	timelineMatchID := strings.TrimSpace(timeline.Metadata.MatchID)
	if timelineMatchID != "" && timelineMatchID != match.RiotMatchID {
		return fmt.Errorf("%w: timeline match ID does not match detail", ErrInvalidMatch)
	}
	if len(timeline.Info.Participants) > 0 {
		found := false
		for _, participant := range timeline.Info.Participants {
			if participant.ParticipantID == match.Stats.ParticipantID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: player participant is missing from timeline", ErrInvalidMatch)
		}
	}

	events := make([]NormalizedTimelineEvent, 0)
	skillOrder := make([]int, 0, 18)
	sequence := 0
	for _, frame := range timeline.Info.Frames {
		for _, event := range frame.Events {
			sequence++
			if event.Type == "SKILL_LEVEL_UP" &&
				event.ParticipantID == match.Stats.ParticipantID &&
				event.SkillSlot > 0 {
				skillOrder = append(skillOrder, event.SkillSlot)
			}
			if !selectedTimelineEvent(event.Type) {
				continue
			}
			normalized, err := normalizeTimelineEvent(sequence, event)
			if err != nil {
				return err
			}
			events = append(events, normalized)
		}
	}

	match.Events = events
	match.Stats.SkillOrder = skillOrder
	match.Completeness = "complete"
	return nil
}

func participantByPUUID(participants []riot.MatchParticipant, puuid string) (riot.MatchParticipant, bool) {
	for _, participant := range participants {
		if participant.PUUID == puuid {
			return participant, true
		}
	}
	return riot.MatchParticipant{}, false
}

func participantRole(participant riot.MatchParticipant) string {
	for _, candidate := range []string{
		participant.TeamPosition,
		participant.IndividualPosition,
	} {
		candidate = strings.ToUpper(strings.TrimSpace(candidate))
		if candidate != "" && candidate != "INVALID" {
			return candidate
		}
	}
	return "UNKNOWN"
}

func opponentChampion(
	participants []riot.MatchParticipant,
	player riot.MatchParticipant,
	role string,
) string {
	if role == "UNKNOWN" {
		return ""
	}
	candidates := make([]string, 0, 1)
	for _, participant := range participants {
		if participant.TeamID == player.TeamID || participantRole(participant) != role {
			continue
		}
		name := strings.TrimSpace(participant.ChampionName)
		if name != "" {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	return candidates[0]
}

func participantRunes(perks riot.Perks) []int {
	runes := make([]int, 0, 12)
	for _, style := range perks.Styles {
		for _, selection := range style.Selections {
			if selection.Perk > 0 {
				runes = append(runes, selection.Perk)
			}
		}
	}
	return append(runes, compactPositive([]int{
		perks.StatPerks.Offense,
		perks.StatPerks.Flex,
		perks.StatPerks.Defense,
	})...)
}

func normalizedDuration(gameDuration int64, timePlayed int) int {
	// Very old Match-V5 payloads occasionally represented duration in
	// milliseconds. A League match cannot realistically last 24 hours.
	if gameDuration > 24*60*60 {
		gameDuration /= 1000
	}
	if gameDuration == 0 && timePlayed > 0 {
		return timePlayed
	}
	return int(gameDuration)
}

func validateParticipantNumbers(participant riot.MatchParticipant) error {
	values := []int{
		participant.Kills,
		participant.Deaths,
		participant.Assists,
		participant.TotalMinionsKilled,
		participant.NeutralMinionsKilled,
		participant.GoldEarned,
		participant.TotalDamageDealtToChampions,
		participant.VisionScore,
		participant.WardsPlaced,
		participant.WardsKilled,
		participant.VisionWardsBoughtInGame,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%w: participant statistics contain a negative value", ErrInvalidMatch)
		}
	}
	return nil
}

func compactPositive(values []int) []int {
	compacted := make([]int, 0, len(values))
	for _, value := range values {
		if value > 0 {
			compacted = append(compacted, value)
		}
	}
	return compacted
}

func patchLabel(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return strings.TrimSpace(version)
	}
	return parts[0] + "." + parts[1]
}

func queueLabel(queueID int) string {
	switch queueID {
	case 400:
		return "Normal Draft"
	case 420:
		return "Ranked Solo"
	case 430:
		return "Normal Blind"
	case 440:
		return "Ranked Flex"
	case 450:
		return "ARAM"
	case 490:
		return "Quickplay"
	case 1700, 1710:
		return "Arena"
	default:
		if queueID == 0 {
			return "Unknown"
		}
		return fmt.Sprintf("Queue %d", queueID)
	}
}

func selectedTimelineEvent(eventType string) bool {
	switch eventType {
	case "CHAMPION_KILL",
		"WARD_PLACED",
		"WARD_KILL",
		"ITEM_PURCHASED",
		"ITEM_SOLD",
		"ITEM_UNDO",
		"SKILL_LEVEL_UP",
		"ELITE_MONSTER_KILL",
		"BUILDING_KILL":
		return true
	default:
		return false
	}
}

func normalizeTimelineEvent(sequence int, event riot.TimelineEvent) (NormalizedTimelineEvent, error) {
	var actor, victim, team *int
	switch event.Type {
	case "CHAMPION_KILL":
		actor = positiveIntPointer(event.KillerID)
		victim = positiveIntPointer(event.VictimID)
	case "WARD_PLACED":
		actor = positiveIntPointer(event.CreatorID)
	case "WARD_KILL":
		actor = positiveIntPointer(event.KillerID)
	case "ITEM_PURCHASED", "ITEM_SOLD", "ITEM_UNDO", "SKILL_LEVEL_UP":
		actor = positiveIntPointer(event.ParticipantID)
	case "ELITE_MONSTER_KILL":
		actor = positiveIntPointer(event.KillerID)
		team = positiveIntPointer(event.KillerTeamID)
		if team == nil {
			team = positiveIntPointer(event.TeamID)
		}
	case "BUILDING_KILL":
		actor = positiveIntPointer(event.KillerID)
		team = positiveIntPointer(event.TeamID)
	default:
		return NormalizedTimelineEvent{}, fmt.Errorf("%w: unsupported timeline event", ErrInvalidMatch)
	}

	var positionX, positionY *int
	if event.Position.X != 0 || event.Position.Y != 0 {
		positionX = intPointer(event.Position.X)
		positionY = intPointer(event.Position.Y)
	}

	data := map[string]any{
		"assistingParticipantIds": event.AssistingParticipantIDs,
		"beforeId":                event.BeforeID,
		"afterId":                 event.AfterID,
		"buildingType":            event.BuildingType,
		"itemId":                  event.ItemID,
		"killType":                event.KillType,
		"laneType":                event.LaneType,
		"level":                   event.Level,
		"levelUpType":             event.LevelUpType,
		"monsterType":             event.MonsterType,
		"monsterSubType":          event.MonsterSubType,
		"skillSlot":               event.SkillSlot,
		"towerType":               event.TowerType,
		"wardType":                event.WardType,
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return NormalizedTimelineEvent{}, fmt.Errorf("%w: encode timeline event: %v", ErrInvalidMatch, err)
	}

	return NormalizedTimelineEvent{
		SequenceNumber:      sequence,
		TimestampMS:         event.Timestamp,
		EventType:           event.Type,
		ActorParticipantID:  actor,
		VictimParticipantID: victim,
		TeamID:              team,
		PositionX:           positionX,
		PositionY:           positionY,
		DataJSON:            string(raw),
	}, nil
}

func positiveIntPointer(value int) *int {
	if value < 1 {
		return nil
	}
	return intPointer(value)
}

func intPointer(value int) *int {
	return &value
}
