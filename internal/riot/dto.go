package riot

// Account is the ACCOUNT-V1 identity resolved from a Riot ID.
type Account struct {
	PUUID    string `json:"puuid"`
	GameName string `json:"gameName"`
	TagLine  string `json:"tagLine"`
}

// Match is the subset of a MATCH-V5 detail response Journalol currently
// normalizes. Unknown JSON fields remain forward-compatible.
type Match struct {
	Metadata MatchMetadata `json:"metadata"`
	Info     MatchInfo     `json:"info"`
}

type MatchMetadata struct {
	DataVersion  string   `json:"dataVersion"`
	MatchID      string   `json:"matchId"`
	Participants []string `json:"participants"`
}

type MatchInfo struct {
	EndOfGameResult    string             `json:"endOfGameResult"`
	GameCreation       int64              `json:"gameCreation"`
	GameDuration       int64              `json:"gameDuration"`
	GameEndTimestamp   int64              `json:"gameEndTimestamp"`
	GameID             int64              `json:"gameId"`
	GameMode           string             `json:"gameMode"`
	GameName           string             `json:"gameName"`
	GameStartTimestamp int64              `json:"gameStartTimestamp"`
	GameType           string             `json:"gameType"`
	GameVersion        string             `json:"gameVersion"`
	MapID              int                `json:"mapId"`
	Participants       []MatchParticipant `json:"participants"`
	PlatformID         string             `json:"platformId"`
	QueueID            int                `json:"queueId"`
	Teams              []MatchTeam        `json:"teams"`
	TournamentCode     string             `json:"tournamentCode"`
}

type MatchParticipant struct {
	Assists                     int    `json:"assists"`
	ChampionID                  int    `json:"championId"`
	ChampionName                string `json:"championName"`
	Deaths                      int    `json:"deaths"`
	GameEndedInEarlySurrender   bool   `json:"gameEndedInEarlySurrender"`
	GameEndedInSurrender        bool   `json:"gameEndedInSurrender"`
	GoldEarned                  int    `json:"goldEarned"`
	IndividualPosition          string `json:"individualPosition"`
	Item0                       int    `json:"item0"`
	Item1                       int    `json:"item1"`
	Item2                       int    `json:"item2"`
	Item3                       int    `json:"item3"`
	Item4                       int    `json:"item4"`
	Item5                       int    `json:"item5"`
	Item6                       int    `json:"item6"`
	Kills                       int    `json:"kills"`
	Lane                        string `json:"lane"`
	NeutralMinionsKilled        int    `json:"neutralMinionsKilled"`
	ParticipantID               int    `json:"participantId"`
	Perks                       Perks  `json:"perks"`
	PUUID                       string `json:"puuid"`
	RiotIDGameName              string `json:"riotIdGameName"`
	RiotIDTagline               string `json:"riotIdTagline"`
	Role                        string `json:"role"`
	Summoner1ID                 int    `json:"summoner1Id"`
	Summoner2ID                 int    `json:"summoner2Id"`
	TeamID                      int    `json:"teamId"`
	TeamPosition                string `json:"teamPosition"`
	TimePlayed                  int    `json:"timePlayed"`
	TotalDamageDealtToChampions int    `json:"totalDamageDealtToChampions"`
	TotalMinionsKilled          int    `json:"totalMinionsKilled"`
	VisionScore                 int    `json:"visionScore"`
	VisionWardsBoughtInGame     int    `json:"visionWardsBoughtInGame"`
	WardsKilled                 int    `json:"wardsKilled"`
	WardsPlaced                 int    `json:"wardsPlaced"`
	Win                         bool   `json:"win"`
}

type Perks struct {
	StatPerks PerkStats   `json:"statPerks"`
	Styles    []PerkStyle `json:"styles"`
}

type PerkStats struct {
	Defense int `json:"defense"`
	Flex    int `json:"flex"`
	Offense int `json:"offense"`
}

type PerkStyle struct {
	Description string          `json:"description"`
	Selections  []PerkSelection `json:"selections"`
	Style       int             `json:"style"`
}

type PerkSelection struct {
	Perk int `json:"perk"`
	Var1 int `json:"var1"`
	Var2 int `json:"var2"`
	Var3 int `json:"var3"`
}

type MatchTeam struct {
	Bans       []Ban          `json:"bans"`
	Objectives TeamObjectives `json:"objectives"`
	TeamID     int            `json:"teamId"`
	Win        bool           `json:"win"`
}

type Ban struct {
	ChampionID int `json:"championId"`
	PickTurn   int `json:"pickTurn"`
}

type TeamObjectives struct {
	Atakhan    Objective `json:"atakhan"`
	Baron      Objective `json:"baron"`
	Champion   Objective `json:"champion"`
	Dragon     Objective `json:"dragon"`
	Horde      Objective `json:"horde"`
	Inhibitor  Objective `json:"inhibitor"`
	RiftHerald Objective `json:"riftHerald"`
	Tower      Objective `json:"tower"`
}

type Objective struct {
	First bool `json:"first"`
	Kills int  `json:"kills"`
}

// Timeline is the portion of a MATCH-V5 timeline response needed for event
// normalization and time-specific metrics.
type Timeline struct {
	Metadata TimelineMetadata `json:"metadata"`
	Info     TimelineInfo     `json:"info"`
}

type TimelineMetadata struct {
	DataVersion  string   `json:"dataVersion"`
	MatchID      string   `json:"matchId"`
	Participants []string `json:"participants"`
}

type TimelineInfo struct {
	EndOfGameResult string                `json:"endOfGameResult"`
	FrameInterval   int64                 `json:"frameInterval"`
	Frames          []TimelineFrame       `json:"frames"`
	GameID          int64                 `json:"gameId"`
	Participants    []TimelineParticipant `json:"participants"`
}

type TimelineParticipant struct {
	ParticipantID int    `json:"participantId"`
	PUUID         string `json:"puuid"`
}

type TimelineFrame struct {
	Events            []TimelineEvent             `json:"events"`
	ParticipantFrames map[string]ParticipantFrame `json:"participantFrames"`
	Timestamp         int64                       `json:"timestamp"`
}

type TimelineEvent struct {
	AfterID                 int              `json:"afterId"`
	AssistingParticipantIDs []int            `json:"assistingParticipantIds"`
	BeforeID                int              `json:"beforeId"`
	Bounty                  int              `json:"bounty"`
	BuildingType            string           `json:"buildingType"`
	CreatorID               int              `json:"creatorId"`
	ItemID                  int              `json:"itemId"`
	KillerID                int              `json:"killerId"`
	KillerTeamID            int              `json:"killerTeamId"`
	KillType                string           `json:"killType"`
	LaneType                string           `json:"laneType"`
	Level                   int              `json:"level"`
	LevelUpType             string           `json:"levelUpType"`
	MonsterSubType          string           `json:"monsterSubType"`
	MonsterType             string           `json:"monsterType"`
	MultiKillLength         int              `json:"multiKillLength"`
	ParticipantID           int              `json:"participantId"`
	Position                Position         `json:"position"`
	RealTimestamp           int64            `json:"realTimestamp"`
	ShutdownBounty          int              `json:"shutdownBounty"`
	SkillSlot               int              `json:"skillSlot"`
	TeamID                  int              `json:"teamId"`
	Timestamp               int64            `json:"timestamp"`
	TowerType               string           `json:"towerType"`
	Type                    string           `json:"type"`
	VictimID                int              `json:"victimId"`
	WardType                string           `json:"wardType"`
	VictimDamageDealt       []ChampionDamage `json:"victimDamageDealt"`
	VictimDamageReceived    []ChampionDamage `json:"victimDamageReceived"`
}

type Position struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type ChampionDamage struct {
	Basic          bool   `json:"basic"`
	MagicDamage    int    `json:"magicDamage"`
	Name           string `json:"name"`
	ParticipantID  int    `json:"participantId"`
	PhysicalDamage int    `json:"physicalDamage"`
	SpellName      string `json:"spellName"`
	SpellSlot      int    `json:"spellSlot"`
	TrueDamage     int    `json:"trueDamage"`
	Type           string `json:"type"`
}

type ParticipantFrame struct {
	ChampionStats            ChampionStats `json:"championStats"`
	CurrentGold              int           `json:"currentGold"`
	DamageStats              DamageStats   `json:"damageStats"`
	GoldPerSecond            int           `json:"goldPerSecond"`
	JungleMinionsKilled      int           `json:"jungleMinionsKilled"`
	Level                    int           `json:"level"`
	MinionsKilled            int           `json:"minionsKilled"`
	ParticipantID            int           `json:"participantId"`
	Position                 Position      `json:"position"`
	TimeEnemySpentControlled int           `json:"timeEnemySpentControlled"`
	TotalGold                int           `json:"totalGold"`
	XP                       int           `json:"xp"`
}

type ChampionStats struct {
	AbilityHaste         int `json:"abilityHaste"`
	AbilityPower         int `json:"abilityPower"`
	Armor                int `json:"armor"`
	ArmorPen             int `json:"armorPen"`
	ArmorPenPercent      int `json:"armorPenPercent"`
	AttackDamage         int `json:"attackDamage"`
	AttackSpeed          int `json:"attackSpeed"`
	BonusArmorPenPercent int `json:"bonusArmorPenPercent"`
	BonusMagicPenPercent int `json:"bonusMagicPenPercent"`
	Health               int `json:"health"`
	HealthMax            int `json:"healthMax"`
	HealthRegen          int `json:"healthRegen"`
	Lifesteal            int `json:"lifesteal"`
	MagicPen             int `json:"magicPen"`
	MagicPenPercent      int `json:"magicPenPercent"`
	MagicResist          int `json:"magicResist"`
	MovementSpeed        int `json:"movementSpeed"`
	Omnivamp             int `json:"omnivamp"`
	PhysicalVamp         int `json:"physicalVamp"`
	Power                int `json:"power"`
	PowerMax             int `json:"powerMax"`
	PowerRegen           int `json:"powerRegen"`
	SpellVamp            int `json:"spellVamp"`
}

type DamageStats struct {
	MagicDamageDone               int `json:"magicDamageDone"`
	MagicDamageDoneToChampions    int `json:"magicDamageDoneToChampions"`
	MagicDamageTaken              int `json:"magicDamageTaken"`
	PhysicalDamageDone            int `json:"physicalDamageDone"`
	PhysicalDamageDoneToChampions int `json:"physicalDamageDoneToChampions"`
	PhysicalDamageTaken           int `json:"physicalDamageTaken"`
	TotalDamageDone               int `json:"totalDamageDone"`
	TotalDamageDoneToChampions    int `json:"totalDamageDoneToChampions"`
	TotalDamageTaken              int `json:"totalDamageTaken"`
	TrueDamageDone                int `json:"trueDamageDone"`
	TrueDamageDoneToChampions     int `json:"trueDamageDoneToChampions"`
	TrueDamageTaken               int `json:"trueDamageTaken"`
}

// MatchPayload retains the exact successful HTTP response alongside its
// decoded representation so the importer can persist raw data first.
type MatchPayload struct {
	Raw   []byte
	Match Match
}

// TimelinePayload retains the exact successful HTTP response alongside its
// decoded representation so timeline import can fail independently.
type TimelinePayload struct {
	Raw      []byte
	Timeline Timeline
}
