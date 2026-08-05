package store

import (
	"context"
	"testing"

	"journalol/internal/model"
)

func TestDeathEventsAndClipLifecycle(t *testing.T) {
	t.Parallel()
	dataStore, _ := openTestStore(t)
	ctx := context.Background()
	player := saveImportTestPlayer(t, dataStore)
	input := importedMatchFixture(player.ID)
	input.Completeness = MatchCompletenessComplete
	input.ReplaceTimeline = true
	input.TimelineEvents = []ImportedTimelineEvent{
		{SequenceNumber: 9, TimestampMS: 90_000, EventType: "CHAMPION_KILL", VictimParticipantID: intPointer(2)},
		{SequenceNumber: 10, TimestampMS: 120_000, EventType: "CHAMPION_KILL", VictimParticipantID: intPointer(7), PositionX: intPointer(100), PositionY: intPointer(200)},
		{SequenceNumber: 11, TimestampMS: 300_000, EventType: "CHAMPION_KILL", VictimParticipantID: intPointer(7)},
	}
	matchID, err := dataStore.UpsertImportedMatch(ctx, input)
	if err != nil {
		t.Fatalf("UpsertImportedMatch(): %v", err)
	}
	subject, err := dataStore.ReplaySubjectForMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("ReplaySubjectForMatch(): %v", err)
	}
	if subject.ParticipantID != 7 || subject.TeamID != 200 || subject.Champion != input.Stats.ChampionName {
		t.Fatalf("replay subject = %#v", subject)
	}

	deaths, err := dataStore.DeathEventsForMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("DeathEventsForMatch(): %v", err)
	}
	if len(deaths) != 2 || deaths[0].SequenceNumber != 10 || deaths[0].TimestampMS != 120_000 ||
		deaths[0].PositionX == nil || *deaths[0].PositionX != 100 || deaths[1].SequenceNumber != 11 {
		t.Fatalf("death events = %#v", deaths)
	}

	clip := model.DeathClip{
		MatchID: matchID, TimelineSeq: 10, DeathIndex: 1,
		DeathTimestamp: 120_000, StartTimestamp: 60_000, EndTimestamp: 135_000,
		ReplayPath: "/replays/NA1-24680.rofl", OutputPath: "/clips/death-01.webm",
		Codec: "webm", Status: model.DeathClipRecording,
	}
	saved, err := dataStore.SaveDeathClip(ctx, clip)
	if err != nil {
		t.Fatalf("SaveDeathClip(recording): %v", err)
	}
	clip.Status = model.DeathClipReady
	ready, err := dataStore.SaveDeathClip(ctx, clip)
	if err != nil {
		t.Fatalf("SaveDeathClip(ready): %v", err)
	}
	if ready.ID != saved.ID || ready.Status != model.DeathClipReady || ready.CreatedAt.IsZero() {
		t.Fatalf("ready clip = %#v, initial = %#v", ready, saved)
	}
}
