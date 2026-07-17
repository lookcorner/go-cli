package tools

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	pdfreader "github.com/ledongthuc/pdf"
)

const (
	pdfAutoReadPages = 10
	pdfMaxReadPages  = 20
)

func extractPDFText(data []byte, pages string) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text = ""
			err = fmt.Errorf("parse PDF: %v", recovered)
		}
	}()
	document, err := pdfreader.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open PDF: %w", err)
	}
	pageCount := document.NumPage()
	if pageCount == 0 {
		return "", errors.New("PDF has no pages")
	}
	selected, err := parsePDFPages(pages, pageCount)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	for index, pageNumber := range selected {
		if index > 0 {
			output.WriteByte('\n')
		}
		fmt.Fprintf(&output, "--- Page %d ---\n", pageNumber)
		pageText, pageErr := document.Page(pageNumber).GetPlainText(nil)
		if pageErr != nil {
			fmt.Fprintf(&output, "[Failed to extract text from page %d: %v]", pageNumber, pageErr)
			continue
		}
		output.WriteString(strings.TrimSpace(pageText))
	}
	return output.String(), nil
}

func parsePDFPages(spec string, pageCount int) ([]int, error) {
	if strings.TrimSpace(spec) == "" {
		if pageCount > pdfAutoReadPages {
			return nil, fmt.Errorf("PDF has %d pages which exceeds the %d page auto-read limit; use pages to select at most %d pages", pageCount, pdfAutoReadPages, pdfMaxReadPages)
		}
		pages := make([]int, pageCount)
		for index := range pages {
			pages[index] = index + 1
		}
		return pages, nil
	}
	seen := make(map[int]bool)
	var pages []int
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		start, end := 0, 0
		if left, right, found := strings.Cut(item, "-"); found {
			var err error
			start, err = strconv.Atoi(strings.TrimSpace(left))
			if err != nil {
				return nil, fmt.Errorf("invalid page number %q", strings.TrimSpace(left))
			}
			if strings.TrimSpace(right) == "" {
				end = pageCount
			} else if end, err = strconv.Atoi(strings.TrimSpace(right)); err != nil {
				return nil, fmt.Errorf("invalid page number %q", strings.TrimSpace(right))
			}
			if start > end {
				return nil, fmt.Errorf("invalid page range %d-%d", start, end)
			}
		} else {
			var err error
			start, err = strconv.Atoi(item)
			if err != nil {
				return nil, fmt.Errorf("invalid page number %q", item)
			}
			end = start
		}
		if start < 1 || start > pageCount {
			return nil, fmt.Errorf("page %d out of range (document has %d pages)", start, pageCount)
		}
		end = min(end, pageCount)
		for page := start; page <= end; page++ {
			if !seen[page] {
				seen[page] = true
				pages = append(pages, page)
			}
		}
	}
	sort.Ints(pages)
	if len(pages) == 0 {
		return nil, errors.New("no pages specified")
	}
	if len(pages) > pdfMaxReadPages {
		return nil, fmt.Errorf("requested %d pages, maximum is %d per call", len(pages), pdfMaxReadPages)
	}
	return pages, nil
}
