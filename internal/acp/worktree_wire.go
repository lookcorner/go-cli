package acp

import (
	"time"

	"github.com/lookcorner/go-cli/internal/worktree"
)

type worktreeRecordWire struct {
	ID             string  `json:"id"`
	Path           string  `json:"path"`
	SourceRepo     string  `json:"source_repo"`
	RepoName       string  `json:"repo_name"`
	Kind           string  `json:"kind"`
	CreationMode   string  `json:"creation_mode"`
	GitRef         *string `json:"git_ref"`
	HeadCommit     *string `json:"head_commit"`
	SessionID      *string `json:"session_id"`
	CreatorPID     *int    `json:"creator_pid"`
	CreatedAt      int64   `json:"created_at"`
	LastAccessedAt *int64  `json:"last_accessed_at"`
	Status         string  `json:"status"`
	Metadata       any     `json:"metadata"`
}

type worktreeStatsWire struct {
	TotalRecords uint64 `json:"total_records"`
	AliveCount   uint64 `json:"alive_count"`
	DeadCount    uint64 `json:"dead_count"`
	DBFileBytes  uint64 `json:"db_file_bytes"`
}

type worktreeGCWire struct {
	DeadRemoved    uint64 `json:"dead_removed"`
	ExpiredRemoved uint64 `json:"expired_removed"`
	SkippedAlive   uint64 `json:"skipped_alive"`
	RemoveFailed   uint64 `json:"remove_failed"`
}

type worktreeRebuildWire struct {
	Discovered     uint64 `json:"discovered"`
	Registered     uint64 `json:"registered"`
	AlreadyTracked uint64 `json:"already_tracked"`
}

func worktreeRecordWires(records []worktree.Record) []worktreeRecordWire {
	result := make([]worktreeRecordWire, 0, len(records))
	for _, record := range records {
		result = append(result, worktreeRecordToWire(record))
	}
	return result
}

func worktreeRecordToWire(record worktree.Record) worktreeRecordWire {
	return worktreeRecordWire{
		ID: record.ID, Path: record.Path, SourceRepo: record.SourceRepo, RepoName: record.RepoName,
		Kind: record.Kind, CreationMode: record.CreationMode, GitRef: optionalString(record.GitRef),
		HeadCommit: optionalString(record.HeadCommit), SessionID: optionalString(record.SessionID),
		CreatorPID: optionalInt(record.CreatorPID), CreatedAt: record.CreatedAt.Unix(),
		LastAccessedAt: optionalTime(record.LastAccessedAt), Status: record.Status, Metadata: nil,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalTime(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	unix := value.Unix()
	return &unix
}

func worktreeStatsToWire(stats worktree.DBStats) worktreeStatsWire {
	return worktreeStatsWire{
		TotalRecords: stats.TotalRecords, AliveCount: stats.AliveCount,
		DeadCount: stats.DeadCount, DBFileBytes: stats.DBFileBytes,
	}
}

func worktreeGCToWire(report worktree.GCReport) worktreeGCWire {
	return worktreeGCWire{
		DeadRemoved: report.DeadRemoved, ExpiredRemoved: report.ExpiredRemoved,
		SkippedAlive: report.SkippedAlive, RemoveFailed: report.RemoveFailed,
	}
}

func worktreeRebuildToWire(report worktree.RebuildReport) worktreeRebuildWire {
	return worktreeRebuildWire{
		Discovered: report.Discovered, Registered: report.Registered, AlreadyTracked: report.AlreadyTracked,
	}
}
