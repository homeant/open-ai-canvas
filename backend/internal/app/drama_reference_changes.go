package app

import (
	"strings"

	"yingce/backend/internal/model"
	"yingce/backend/internal/production"
)

type dramaShotImagePlan struct {
	Request DramaSegmentRequest
	Error   error
}

// Both dry-run and saved edit receipts compare the actual provider preparation,
// including capacity, first-frame offsets and references that were not sent.
func (s *Service) dramaShotImagePlans(p *model.DramaProduction, shots []model.DramaShot, casts map[string]model.DramaCast, segments []model.DramaSegment, indices map[int]bool) map[int]dramaShotImagePlan {
	settings := parseDramaSettings(p.SettingsJSON)
	drafts := buildDramaSegmentDrafts(settings, parseDramaBibleMeta(p.BibleJSON), shots, casts, segments)
	byIndex := map[int]dramaSegmentDraft{}
	for _, draft := range drafts {
		byIndex[draft.view.Index] = draft
	}
	plans := map[int]dramaShotImagePlan{}
	for i, row := range segments {
		if !indices[row.SegmentIndex] {
			continue
		}
		draft, ok := byIndex[row.SegmentIndex]
		if !ok {
			continue
		}
		var previous *model.DramaSegment
		if i > 0 {
			previous = &segments[i-1]
		}
		request, _, _, err := s.prepareDramaSegment(settings, draft, row, previous, casts)
		plans[row.SegmentIndex] = dramaShotImagePlan{Request: request, Error: err}
	}
	return plans
}

type dramaReferenceNumberingChange struct {
	Before *DramaSegmentReference `json:"before"`
	After  *DramaSegmentReference `json:"after"`
	Change string                 `json:"change"`
}
type dramaReferencePromptConflict struct {
	Token              string `json:"token"`
	PreviousResourceID string `json:"previousResourceId"`
	CurrentResourceID  string `json:"currentResourceId"`
	SuggestedLabel     string `json:"suggestedLabel"`
	Reason             string `json:"reason"`
}
type dramaReferenceNumberingReport struct {
	SegmentIndex               int                             `json:"segmentIndex"`
	NumberingChanged           bool                            `json:"numberingChanged"`
	PromptReviewRequired       bool                            `json:"promptReviewRequired"`
	ComparisonComplete         bool                            `json:"comparisonComplete"`
	BeforePreparationError     string                          `json:"beforePreparationError"`
	AfterPreparationError      string                          `json:"afterPreparationError"`
	BeforeFirstFrameResourceID string                          `json:"beforeFirstFrameResourceId"`
	AfterFirstFrameResourceID  string                          `json:"afterFirstFrameResourceId"`
	Mapping                    []dramaReferenceNumberingChange `json:"mapping"`
	PromptConflicts            []dramaReferencePromptConflict  `json:"promptConflicts"`
	Warnings                   []string                        `json:"warnings"`
}

// A cast and a shot-specific information image may use the same physical file.
// Match their semantic identities separately, and preserve repeated occurrences.
func dramaReferenceIdentities(refs []DramaSegmentReference) []string {
	ids := make([]string, 0, len(refs))
	seen := map[string]int{}
	for _, ref := range refs {
		identity := []string{"resource", ref.Kind, ref.Role, ref.ResourceID}
		if ref.CastID != "" {
			identity = []string{"cast", ref.CastID}
		} else if ref.ShotID != "" {
			identity = []string{"shot", ref.ShotID, ref.Role, ref.ResourceID}
		}
		key := dramaContentRevision(identity)
		seen[key]++
		ids = append(ids, dramaContentRevision([]any{key, seen[key]}))
	}
	return ids
}

func dramaReferenceNumberingChanges(index int, before, after dramaShotImagePlan, prompt string) dramaReferenceNumberingReport {
	report := dramaReferenceNumberingReport{SegmentIndex: index, ComparisonComplete: before.Error == nil && after.Error == nil, BeforeFirstFrameResourceID: before.Request.FirstFrameResourceID, AfterFirstFrameResourceID: after.Request.FirstFrameResourceID, Mapping: []dramaReferenceNumberingChange{}, PromptConflicts: []dramaReferencePromptConflict{}, Warnings: []string{}}
	if before.Error != nil {
		report.BeforePreparationError = before.Error.Error()
	}
	if after.Error != nil {
		report.AfterPreparationError = after.Error.Error()
	}
	oldIDs, newIDs := dramaReferenceIdentities(before.Request.References), dramaReferenceIdentities(after.Request.References)
	afterByID := map[string]DramaSegmentReference{}
	afterByLabel := map[string]DramaSegmentReference{}
	for i, ref := range after.Request.References {
		afterByID[newIDs[i]] = ref
		afterByLabel[ref.Label] = ref
	}
	byOldLabel := map[string]dramaReferenceNumberingChange{}
	for i, ref := range before.Request.References {
		change := dramaReferenceNumberingChange{Before: &ref, Change: "removed"}
		if current, ok := afterByID[oldIDs[i]]; ok {
			change.After = &current
			change.Change = "unchanged"
			switch {
			case ref.Label != current.Label:
				change.Change = "renumbered"
			case ref.ResourceID != current.ResourceID:
				change.Change = "resource_changed"
			case ref.Sent != current.Sent:
				change.Change = "delivery_changed"
			case ref.InputOrder != current.InputOrder:
				change.Change = "input_order_changed"
			}
			delete(afterByID, oldIDs[i])
		}
		if change.After == nil || ref.Label != change.After.Label {
			report.NumberingChanged = true
		}
		if change.Change != "unchanged" {
			report.PromptReviewRequired = true
		}
		report.Mapping = append(report.Mapping, change)
		byOldLabel[ref.Label] = change
	}
	for i, ref := range after.Request.References {
		if _, ok := afterByID[newIDs[i]]; ok {
			report.Mapping = append(report.Mapping, dramaReferenceNumberingChange{After: &ref, Change: "added"})
			report.NumberingChanged = true
			report.PromptReviewRequired = true
		}
	}
	// With count=0 the existing tokenizer returns every numbered reference,
	// preserving the same normalization and token boundaries as prompt admission.
	for _, token := range production.UnknownReferenceTokens(prompt, 0) {
		change, known := byOldLabel[token]
		if known && (change.Change == "unchanged" || change.Change == "input_order_changed") {
			continue
		}
		conflict := dramaReferencePromptConflict{Token: token, Reason: "previous_reference_unknown"}
		if current, ok := afterByLabel[token]; ok {
			conflict.CurrentResourceID = current.ResourceID
		}
		if known {
			conflict.PreviousResourceID = change.Before.ResourceID
			conflict.Reason = change.Change
			if change.After != nil && change.After.Sent {
				conflict.SuggestedLabel = change.After.Label
			}
		}
		report.PromptConflicts = append(report.PromptConflicts, conflict)
	}
	if report.NumberingChanged {
		report.Warnings = append(report.Warnings, "参考图编号已变化，请按mapping逐项核对旧提示词；不会自动改写或确认正文")
	}
	if len(report.PromptConflicts) > 0 {
		tokens := []string{}
		for _, conflict := range report.PromptConflicts {
			tokens = append(tokens, conflict.Token)
		}
		report.Warnings = append(report.Warnings, "旧提示词引用需复核："+strings.Join(tokens, "、"))
		report.PromptReviewRequired = true
	}
	if report.BeforeFirstFrameResourceID != report.AfterFirstFrameResourceID {
		report.Warnings = append(report.Warnings, "首帧资源已变化，首帧不占参考图编号，请单独复核")
		report.PromptReviewRequired = true
	}
	if !report.ComparisonComplete {
		report.Warnings = append(report.Warnings, "前后请求至少一侧未完整通过参考图准备；mapping可能不完整，请先排除preparationError再复核")
		report.PromptReviewRequired = true
	}
	return report
}
