package agent

import (
	"errors"
	"sync/atomic"

	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type RewindMode string

const (
	RewindAll              RewindMode = "all"
	RewindConversationOnly RewindMode = "conversation_only"
	RewindFilesOnly        RewindMode = "files_only"
)

type RewindPreview struct {
	Target     int
	Mode       RewindMode
	PromptText string
	CleanFiles []string
	Conflicts  []workspace.RewindConflict
}

type RewindResult struct {
	RewindPreview
	PreviousResponseID string
	Messages           []session.Message
	RevertedFiles      []string
}

type rewindRuntime struct {
	store   *workspace.RewindStore
	next    atomic.Int64
	active  atomic.Int64
	enabled atomic.Bool
}

func (r *Runner) EnableRewind(store *workspace.RewindStore, nextPrompt int) error {
	if r == nil || r.Tools == nil || store == nil || nextPrompt < 0 {
		return errors.New("rewind store, tools, and non-negative prompt index are required")
	}
	r.rewind.store = store
	r.rewind.next.Store(int64(nextPrompt))
	r.rewind.active.Store(-1)
	r.rewind.enabled.Store(true)
	r.Tools.SetRewindStore(store, func() int { return int(r.rewind.active.Load()) })
	return nil
}

func (r *Runner) RewindPoints() ([]session.RewindPoint, error) {
	if r == nil || !r.rewind.enabled.Load() || r.rewind.store == nil || r.SessionPath == "" {
		return nil, errors.New("rewind is unavailable")
	}
	if r.rewind.active.Load() >= 0 {
		return nil, errors.New("cannot rewind while a turn is running")
	}
	points, err := session.RewindPoints(r.SessionPath)
	if err != nil {
		return nil, err
	}
	counts, err := r.rewind.store.Counts()
	if err != nil {
		return nil, err
	}
	for index := range points {
		points[index].NumFileSnapshots = counts[points[index].PromptIndex]
		points[index].HasFileChanges = points[index].NumFileSnapshots > 0
	}
	return points, nil
}

func (r *Runner) PreviewRewind(target int, mode RewindMode) (RewindPreview, error) {
	if !validRewindMode(mode) || r == nil || !r.rewind.enabled.Load() || r.rewind.store == nil || r.SessionPath == "" {
		return RewindPreview{}, errors.New("rewind is unavailable or has an invalid mode")
	}
	if r.rewind.active.Load() >= 0 {
		return RewindPreview{}, errors.New("cannot rewind while a turn is running")
	}
	conversation, err := session.PreviewRewind(r.SessionPath, target)
	if err != nil {
		return RewindPreview{}, err
	}
	preview := RewindPreview{Target: target, Mode: mode, PromptText: conversation.PromptText, CleanFiles: []string{}, Conflicts: []workspace.RewindConflict{}}
	if mode == RewindAll || mode == RewindFilesOnly {
		files, err := r.rewind.store.Preview(target)
		if err != nil {
			return RewindPreview{}, err
		}
		preview.CleanFiles, preview.Conflicts = files.CleanFiles, files.Conflicts
	}
	return preview, nil
}

func (r *Runner) ExecuteRewind(target int, mode RewindMode) (RewindResult, error) {
	preview, err := r.PreviewRewind(target, mode)
	if err != nil {
		return RewindResult{}, err
	}
	result := RewindResult{RewindPreview: preview, RevertedFiles: []string{}}
	if mode == RewindAll || mode == RewindFilesOnly {
		result.RevertedFiles, _, err = r.rewind.store.Restore(target)
		if err != nil {
			return result, err
		}
	} else if err := r.rewind.store.MergeFrom(target); err != nil {
		return result, err
	}
	if mode == RewindAll || mode == RewindConversationOnly {
		conversation, rewindErr := session.Rewind(r.SessionPath, target)
		if rewindErr != nil {
			return result, rewindErr
		}
		result.PreviousResponseID, result.Messages = conversation.PreviousResponseID, conversation.Messages
		r.rewind.next.Store(int64(target))
		r.promptEpoch.Add(1)
		r.ClearInterjections()
		r.RestoreHistory(conversation.Messages)
	}
	return result, nil
}

func validRewindMode(mode RewindMode) bool {
	return mode == RewindAll || mode == RewindConversationOnly || mode == RewindFilesOnly
}
