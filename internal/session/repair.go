package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type HistoryRepairReport struct {
	DuplicatesRemoved        int      `json:"duplicatesRemoved"`
	StrippedToolResultIDs    []string `json:"strippedToolResultIds"`
	SyntheticResultsInserted int      `json:"syntheticResultsInserted"`
}

func (r HistoryRepairReport) Changed() bool {
	return r.DuplicatesRemoved > 0 || len(r.StrippedToolResultIDs) > 0 || r.SyntheticResultsInserted > 0
}

func RepairHistory(path string, dryRun bool) (HistoryRepairReport, error) {
	report, data, info, err := repairedHistory(path)
	if err != nil || dryRun || !report.Changed() {
		return report, err
	}
	return report, replaceHistory(path, data, info)
}

func (l *Logger) RepairHistory(dryRun bool) (HistoryRepairReport, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return HistoryRepairReport{}, errors.New("session log is closed")
	}
	if err := l.file.Sync(); err != nil {
		return HistoryRepairReport{}, fmt.Errorf("sync session log: %w", err)
	}
	report, data, info, err := repairedHistory(l.path)
	if err != nil || dryRun || !report.Changed() {
		return report, err
	}
	if err := l.file.Close(); err != nil {
		return report, fmt.Errorf("close session log for repair: %w", err)
	}
	l.file = nil
	if err := replaceHistory(l.path, data, info); err != nil {
		var reopenErr error
		l.file, reopenErr = openStableAppend(l.path, info)
		if reopenErr != nil {
			return report, errors.Join(err, fmt.Errorf("reopen original session log: %w", reopenErr))
		}
		return report, err
	}
	l.file, err = openStableAppend(l.path, nil)
	if err != nil {
		return report, fmt.Errorf("reopen repaired session log: %w", err)
	}
	l.needsNewline = false
	return report, nil
}

func openStableAppend(path string, expected os.FileInfo) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	linked, linkErr := os.Lstat(path)
	stable := statErr == nil && linkErr == nil && linked.Mode()&os.ModeSymlink == 0 && opened.Mode().IsRegular() && os.SameFile(opened, linked)
	if stable && expected != nil {
		stable = os.SameFile(expected, opened)
	}
	if !stable {
		_ = file.Close()
		return nil, errors.New("session log changed before reopen")
	}
	return file, nil
}

func repairedHistory(path string) (HistoryRepairReport, []byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return HistoryRepairReport{}, nil, nil, fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return HistoryRepairReport{}, nil, nil, fmt.Errorf("stat session log: %w", err)
	}
	linked, err := os.Lstat(path)
	if err != nil || linked.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(info, linked) {
		return HistoryRepairReport{}, nil, nil, errors.New("session log must be a stable regular, non-symlink file")
	}
	if info.Size() > maxSessionBytes {
		return HistoryRepairReport{}, nil, nil, fmt.Errorf("session log exceeds %d bytes", maxSessionBytes)
	}

	var events []storedEvent
	scanner := bufio.NewScanner(io.LimitReader(file, maxSessionBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var event storedEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Kind != "" {
			events = append(events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return HistoryRepairReport{}, nil, nil, fmt.Errorf("read session log: %w", err)
	}
	repaired, report := repairHistoryEvents(events)
	if !report.Changed() {
		return report, nil, info, nil
	}
	data := make([]byte, 0, info.Size())
	for _, event := range repaired {
		encoded, err := json.Marshal(event)
		if err != nil {
			return HistoryRepairReport{}, nil, nil, fmt.Errorf("encode repaired session event: %w", err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	return report, data, info, nil
}

func repairHistoryEvents(events []storedEvent) ([]storedEvent, HistoryRepairReport) {
	repaired := make([]storedEvent, 0, len(events))
	var report HistoryRepairReport
	for start := 0; start < len(events); {
		if events[start].Kind != "tool_call" && events[start].Kind != "tool_result" {
			repaired = append(repaired, events[start])
			start++
			continue
		}
		end := start
		for end < len(events) && (events[end].Kind == "tool_call" || events[end].Kind == "tool_result") {
			end++
		}
		segment, segmentReport := repairToolRun(events[start:end])
		repaired = append(repaired, segment...)
		report.DuplicatesRemoved += segmentReport.DuplicatesRemoved
		report.StrippedToolResultIDs = append(report.StrippedToolResultIDs, segmentReport.StrippedToolResultIDs...)
		report.SyntheticResultsInserted += segmentReport.SyntheticResultsInserted
		start = end
	}
	return repaired, report
}

func repairToolRun(events []storedEvent) ([]storedEvent, HistoryRepairReport) {
	var report HistoryRepairReport
	calls := make(map[string]storedEvent)
	var callOrder []string
	lastResult := make(map[string]int)
	keepResult := make(map[int]bool)
	for index, event := range events {
		var data struct {
			CallID string `json:"call_id"`
		}
		_ = json.Unmarshal(event.Data, &data)
		if event.Kind == "tool_call" {
			if data.CallID != "" {
				if _, exists := calls[data.CallID]; !exists {
					callOrder = append(callOrder, data.CallID)
				}
				calls[data.CallID] = event
			}
			continue
		}
		if _, exists := calls[data.CallID]; !exists {
			report.StrippedToolResultIDs = append(report.StrippedToolResultIDs, data.CallID)
			continue
		}
		if previous, exists := lastResult[data.CallID]; exists {
			delete(keepResult, previous)
			report.DuplicatesRemoved++
		}
		lastResult[data.CallID] = index
		keepResult[index] = true
	}

	result := make([]storedEvent, 0, len(events)+len(calls))
	for index, event := range events {
		if event.Kind == "tool_call" || keepResult[index] {
			result = append(result, event)
		}
	}
	for _, callID := range callOrder {
		if _, answered := lastResult[callID]; answered {
			continue
		}
		var call struct {
			Step int    `json:"step"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(calls[callID].Data, &call)
		data, _ := json.Marshal(map[string]any{
			"step": call.Step, "call_id": callID, "name": call.Name,
			"output": fmt.Sprintf("Tool execution was halted by the harness (history_repair); the tool `%s` was not executed.", call.Name),
			"failed": true, "synthetic": true,
		})
		result = append(result, storedEvent{Time: time.Now().UTC(), Kind: "tool_result", Data: data})
		report.SyntheticResultsInserted++
	}
	return result, report
}

func replaceHistory(path string, data []byte, original os.FileInfo) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-repair-*")
	if err != nil {
		return fmt.Errorf("create repaired session log: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(original.Mode().Perm()); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write repaired session log: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(original, current) {
		return errors.New("session log changed during repair")
	}
	if err := atomicReplace(temporaryPath, path); err != nil {
		return fmt.Errorf("replace repaired session log: %w", err)
	}
	return nil
}
