package importer

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"journalol/internal/riot"
)

func TestNormalizeMatchUsesPUUIDAndTeamPosition(t *testing.T) {
	player := riot.MatchParticipant{
		PUUID:                       "primary-puuid",
		ParticipantID:               5,
		TeamID:                      100,
		ChampionID:                  40,
		ChampionName:                "Janna",
		TeamPosition:                "UTILITY",
		IndividualPosition:          "MIDDLE",
		Role:                        "SOLO",
		Win:                         true,
		Kills:                       1,
		Deaths:                      2,
		Assists:                     14,
		TotalMinionsKilled:          20,
		NeutralMinionsKilled:        3,
		GoldEarned:                  9111,
		TotalDamageDealtToChampions: 8200,
		VisionScore:                 55,
		WardsPlaced:                 22,
		WardsKilled:                 5,
		VisionWardsBoughtInGame:     4,
		Item0:                       6617,
		Item1:                       2055,
		Item6:                       3364,
		Summoner1ID:                 4,
		Summoner2ID:                 14,
		Perks: riot.Perks{
			Styles: []riot.PerkStyle{{
				Selections: []riot.PerkSelection{{Perk: 8214}, {Perk: 8237}},
			}},
			StatPerks: riot.PerkStats{Offense: 5008, Flex: 5002, Defense: 5001},
		},
	}
	detail := riot.Match{
		Metadata: riot.MatchMetadata{MatchID: "NA1_900000001"},
		Info: riot.MatchInfo{
			GameStartTimestamp: 1_700_000_000_000,
			GameEndTimestamp:   1_700_001_800_000,
			GameDuration:       1800,
			GameVersion:        "15.14.123.456",
			QueueID:            420,
			MapID:              11,
			GameMode:           "CLASSIC",
			GameType:           "MATCHED_GAME",
			Participants: []riot.MatchParticipant{
				player,
				{PUUID: "enemy", ParticipantID: 10, TeamID: 200, ChampionName: "Nautilus", TeamPosition: "UTILITY"},
				{PUUID: "enemy-mid", ParticipantID: 9, TeamID: 200, ChampionName: "Ahri", TeamPosition: "MIDDLE"},
			},
		},
	}

	got, err := NormalizeMatch(detail, "primary-puuid")
	if err != nil {
		t.Fatalf("NormalizeMatch() error = %v", err)
	}
	if got.RiotMatchID != "NA1_900000001" || got.QueueType != "Ranked Solo" ||
		got.Patch != "15.14" || got.DurationSeconds != 1800 {
		t.Fatalf("match metadata = %#v", got)
	}
	if got.StartedAt != time.UnixMilli(1_700_000_000_000).UTC() ||
		got.EndedAt != time.UnixMilli(1_700_001_800_000).UTC() {
		t.Fatalf("times = %v .. %v", got.StartedAt, got.EndedAt)
	}
	if got.Stats.Role != "UTILITY" || got.Stats.OpponentChampion != "Nautilus" ||
		got.Stats.VisionWardsBought != 4 {
		t.Fatalf("stats = %#v", got.Stats)
	}
	if !reflect.DeepEqual(got.Stats.FinalItems, []int{6617, 2055, 3364}) {
		t.Fatalf("items = %v", got.Stats.FinalItems)
	}
	if !reflect.DeepEqual(got.Stats.Runes, []int{8214, 8237, 5008, 5002, 5001}) {
		t.Fatalf("runes = %v", got.Stats.Runes)
	}
}

func TestNormalizeMatchMarksExplicitRemakeWithoutSurrender(t *testing.T) {
	detail := riot.Match{
		Metadata: riot.MatchMetadata{MatchID: "NA1_remake"},
		Info: riot.MatchInfo{
			GameStartTimestamp: 1_700_000_000_000,
			GameDuration:       210,
			MapID:              11,
			EndOfGameResult:    "EarlySurrender",
			Participants: []riot.MatchParticipant{{
				PUUID:                     "primary",
				ParticipantID:             1,
				ChampionName:              "Lux",
				GameEndedInEarlySurrender: true,
				GameEndedInSurrender:      true,
			}},
		},
	}
	got, err := NormalizeMatch(detail, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRemake || got.Surrendered {
		t.Fatalf("remake/surrendered = %t/%t", got.IsRemake, got.Surrendered)
	}
}

func TestNormalizeMatchDoesNotTreatLongEarlySurrenderAsRemake(t *testing.T) {
	detail := riot.Match{
		Metadata: riot.MatchMetadata{MatchID: "NA1_surrender"},
		Info: riot.MatchInfo{
			GameStartTimestamp: 1_700_000_000_000,
			GameDuration:       15 * 60,
			MapID:              11,
			EndOfGameResult:    "EarlySurrender",
			Participants: []riot.MatchParticipant{{
				PUUID:                     "primary",
				ParticipantID:             1,
				ChampionName:              "Lux",
				GameEndedInEarlySurrender: true,
			}},
		},
	}
	got, err := NormalizeMatch(detail, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsRemake || !got.Surrendered {
		t.Fatalf("remake/surrendered = %t/%t, want false/true", got.IsRemake, got.Surrendered)
	}
}

func TestNormalizeMatchRequiresPrimaryParticipant(t *testing.T) {
	_, err := NormalizeMatch(riot.Match{
		Metadata: riot.MatchMetadata{MatchID: "NA1_missing"},
		Info: riot.MatchInfo{
			GameStartTimestamp: 1_700_000_000_000,
			Participants:       []riot.MatchParticipant{{PUUID: "other", ParticipantID: 1}},
		},
	}, "primary")
	if !errors.Is(err, ErrPlayerNotInGame) {
		t.Fatalf("error = %v, want ErrPlayerNotInGame", err)
	}
}

func TestApplyTimelineKeepsStableSourceSequenceAndSkillOrder(t *testing.T) {
	match := &NormalizedMatch{
		RiotMatchID: "NA1_1",
		Stats: NormalizedPlayerStats{
			ParticipantID: 5,
		},
	}
	timeline := riot.Timeline{
		Metadata: riot.TimelineMetadata{MatchID: "NA1_1"},
		Info: riot.TimelineInfo{
			Participants: []riot.TimelineParticipant{{ParticipantID: 5, PUUID: "primary"}},
			Frames: []riot.TimelineFrame{{Events: []riot.TimelineEvent{
				{Type: "PAUSE_END", Timestamp: 10},
				{Type: "SKILL_LEVEL_UP", Timestamp: 20, ParticipantID: 5, SkillSlot: 2},
				{
					Type:                    "CHAMPION_KILL",
					Timestamp:               30,
					KillerID:                3,
					VictimID:                5,
					AssistingParticipantIDs: []int{4},
					Position:                riot.Position{X: 100, Y: 200},
				},
				{
					Type:                    "ELITE_MONSTER_KILL",
					Timestamp:               40,
					KillerID:                1,
					KillerTeamID:            100,
					AssistingParticipantIDs: []int{5},
					MonsterType:             "DRAGON",
					MonsterSubType:          "AIR_DRAGON",
				},
				{Type: "SKILL_LEVEL_UP", Timestamp: 50, ParticipantID: 7, SkillSlot: 3},
			}}},
		},
	}
	if err := ApplyTimeline(match, timeline); err != nil {
		t.Fatalf("ApplyTimeline() error = %v", err)
	}
	if match.Completeness != "complete" {
		t.Fatalf("completeness = %q", match.Completeness)
	}
	if !reflect.DeepEqual(match.Stats.SkillOrder, []int{2}) {
		t.Fatalf("skill order = %v", match.Stats.SkillOrder)
	}
	if len(match.Events) != 4 {
		t.Fatalf("selected events = %d, want 4", len(match.Events))
	}
	if match.Events[0].SequenceNumber != 2 ||
		match.Events[1].SequenceNumber != 3 ||
		match.Events[2].SequenceNumber != 4 ||
		match.Events[3].SequenceNumber != 5 {
		t.Fatalf("source sequences = %#v", match.Events)
	}
	if match.Events[2].TeamID == nil || *match.Events[2].TeamID != 100 {
		t.Fatalf("elite monster team = %v", match.Events[2].TeamID)
	}
}
