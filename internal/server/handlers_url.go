package server

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"crawlsync/internal/urlmanager"
)

var (
	validStatuses = []string{
		string(urlmanager.StatusPending),
		string(urlmanager.StatusInProgress),
		string(urlmanager.StatusCompleted),
		string(urlmanager.StatusFailed),
	}

	validSortOptions = []string{
		"seq_asc",
		"seq_desc",
		"depth_asc",
		"depth_desc",
		"time_desc",
	}
)

// GET /api/urls
func (h *apiHandler) handleURLList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	opts := urlmanager.URLFilterOptions{
		Limit:  urlmanager.DefaultPaginationLimit,
		Offset: urlmanager.DefaultPaginationOffset,
		SortBy: "seq_asc",
	}

	// 1. Parse limit
	if limitStr := q.Get("limit"); limitStr != "" {
		limitVal, err := strconv.Atoi(limitStr)
		if err != nil || limitVal < 1 || limitVal > urlmanager.MaxPaginationLimit {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY_PARAMETER", fmt.Sprintf("limit must be an integer between 1 and %d", urlmanager.MaxPaginationLimit))
			return
		}
		opts.Limit = limitVal
	}

	// 2. Parse offset
	if offsetStr := q.Get("offset"); offsetStr != "" {
		offsetVal, err := strconv.Atoi(offsetStr)
		if err != nil || offsetVal < urlmanager.MinPaginationOffset {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY_PARAMETER", "offset must be a non-negative integer")
			return
		}
		opts.Offset = offsetVal
	}

	// 3. Parse status filter
	if statusStr := q.Get("status"); statusStr != "" {
		statusLower := strings.ToLower(statusStr)
		if !slices.Contains(validStatuses, statusLower) {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY_PARAMETER", "status must be one of: pending, in_progress, completed, failed")
			return
		}
		st := urlmanager.URLStatus(statusLower)
		opts.Status = &st
	}

	// 4. Parse depth filter
	if depthStr := q.Get("depth"); depthStr != "" {
		depthVal, err := strconv.Atoi(depthStr)
		if err != nil || depthVal < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY_PARAMETER", "depth must be a non-negative integer")
			return
		}
		opts.Depth = &depthVal
	}

	// 5. Parse sort order
	if sortStr := q.Get("sort"); sortStr != "" {
		sortLower := strings.ToLower(sortStr)
		if !slices.Contains(validSortOptions, sortLower) {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY_PARAMETER", "sort must be one of: seq_asc, seq_desc, depth_asc, depth_desc, time_desc")
			return
		}
		opts.SortBy = sortLower
	}

	items, total, filtered, err := h.service.GetURLs(opts)
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	dtos := make([]URLItemResponseDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, toURLItemDTO(it))
	}

	resp := URLListResponseDTO{
		Total:    total,
		Filtered: filtered,
		Limit:    opts.Limit,
		Offset:   opts.Offset,
		Items:    dtos,
	}

	writeJSON(w, http.StatusOK, resp)
}

// GET /api/urls/{id}
func (h *apiHandler) handleURLDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "INVALID_URL_ID", "URL sequence ID is required")
		return
	}

	seq, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || seq <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_URL_ID", "URL sequence ID must be a positive integer")
		return
	}

	item, err := h.service.GetURLBySequence(seq)
	if err != nil {
		h.mapServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toURLItemDTO(*item))
}

func toURLItemDTO(it urlmanager.URLItem) URLItemResponseDTO {
	return URLItemResponseDTO{
		Sequence:     it.Sequence,
		URL:          it.URL,
		Normalized:   it.Normalized,
		Status:       string(it.Status),
		Depth:        it.Depth,
		DiscoveredAt: it.DiscoveredAt,
		UpdatedAt:    it.UpdatedAt,
		StatusCode:   it.StatusCode,
		ErrorMessage: it.ErrorMessage,
		Size:         it.Size,
		DurationMs:   it.DurationMs,
		SavedPath:    it.SavedPath,
	}
}

