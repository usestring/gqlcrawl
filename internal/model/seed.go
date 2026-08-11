package model

type SeedKind string

const (
	SeedHost SeedKind = "host"
	SeedURL  SeedKind = "url"
)

type RankKind string

const (
	RankNone    RankKind = ""
	RankOrdinal RankKind = "ordinal"
	RankBucket  RankKind = "bucket"
	RankMember  RankKind = "member"
)

type Seed struct {
	SchemaVersion string   `json:"schema_version"`
	Value         string   `json:"value"`
	Kind          SeedKind `json:"kind"`
	Adapter       string   `json:"adapter"`
	Rank          int      `json:"rank,omitempty"`
	RankKind      RankKind `json:"rank_kind,omitempty"`
	Evidence      string   `json:"evidence,omitempty"`
}
