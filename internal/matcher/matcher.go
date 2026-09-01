package matcher

import (
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/cases"

	"g2b-monitor/internal/model"
)

type Reason string

const ReasonIncludeAny Reason = "include_any"

const (
	ReasonIncludeAll         Reason = "include_all"
	ReasonExcluded           Reason = "excluded"
	ReasonCategory           Reason = "category"
	ReasonAgency             Reason = "agency"
	ReasonRegion             Reason = "region"
	ReasonMinAmount          Reason = "min_amount"
	ReasonMaxAmount          Reason = "max_amount"
	ReasonDeadlineWeekday    Reason = "deadline_weekday"
	ReasonDeadlineWithinDays Reason = "deadline_within_days"
	ReasonInvalidDeadline    Reason = "invalid_deadline"
)

type Rule struct {
	IncludeAny         []string
	IncludeAll         []string
	Exclude            []string
	Categories         []model.Category
	Agencies           []string
	Regions            []string
	MinAmount          *int64
	MaxAmount          *int64
	DeadlineWeekdays   []time.Weekday
	DeadlineWithinDays *int
}

type Result struct {
	Matched bool
	Reasons []Reason
	Details []Detail
}

type Detail struct {
	Code                          Reason
	Field, RuleValue, NoticeValue string
}

func Match(notice model.Notice, rule Rule) Result {
	return MatchAt(time.Now(), notice, rule)
}

func MatchAt(now time.Time, notice model.Notice, rule Rule) Result {
	if field, term, ok := keywordMatch(notice, rule.Exclude); ok {
		return Result{Reasons: []Reason{ReasonExcluded}, Details: []Detail{{Code: ReasonExcluded, Field: field, RuleValue: term, NoticeValue: detailValue(notice, field)}}}
	}
	result := Result{}
	add := func(code Reason, field, value string) {
		result.Reasons = append(result.Reasons, code)
		result.Details = append(result.Details, Detail{Code: code, Field: field, RuleValue: value, NoticeValue: detailValue(notice, field)})
	}
	if hasTerms(rule.IncludeAny) {
		field, term, ok := keywordMatch(notice, rule.IncludeAny)
		if !ok {
			return Result{}
		}
		add(ReasonIncludeAny, field, term)
	}
	if hasTerms(rule.IncludeAll) {
		for _, term := range rule.IncludeAll {
			field, value, ok := keywordMatch(notice, []string{term})
			if strings.TrimSpace(term) != "" && !ok {
				return Result{}
			}
			if strings.TrimSpace(term) != "" {
				add(ReasonIncludeAll, field, value)
			}
		}
	}
	if len(rule.Categories) > 0 {
		if !hasCategory(notice.Category, rule.Categories) {
			return Result{}
		}
		add(ReasonCategory, "category", string(notice.Category))
	}
	if hasTerms(rule.Agencies) {
		term, ok := matchingTerm(notice.Agency, rule.Agencies)
		if !ok {
			return Result{}
		}
		add(ReasonAgency, "agency", term)
	}
	if hasTerms(rule.Regions) {
		term, ok := matchingTerm(notice.Region, rule.Regions)
		if !ok {
			return Result{}
		}
		add(ReasonRegion, "region", term)
	}
	if rule.MinAmount != nil {
		if notice.Amount < *rule.MinAmount {
			return Result{}
		}
		add(ReasonMinAmount, "amount", strconv.FormatInt(*rule.MinAmount, 10))
	}
	if rule.MaxAmount != nil {
		if notice.Amount > *rule.MaxAmount {
			return Result{}
		}
		add(ReasonMaxAmount, "amount", strconv.FormatInt(*rule.MaxAmount, 10))
	}
	if len(rule.DeadlineWeekdays) > 0 {
		if notice.Deadline.IsZero() {
			return Result{Reasons: []Reason{ReasonInvalidDeadline}, Details: []Detail{{Code: ReasonInvalidDeadline, Field: "deadline", RuleValue: strings.Join(weekdays(rule.DeadlineWeekdays), ","), NoticeValue: ""}}}
		}
		if !hasWeekday(notice.Deadline.Weekday(), rule.DeadlineWeekdays) {
			return Result{}
		}
		add(ReasonDeadlineWeekday, "deadline", strings.Join(weekdays(rule.DeadlineWeekdays), ","))
	}
	if rule.DeadlineWithinDays != nil {
		if notice.Deadline.IsZero() {
			return Result{Reasons: []Reason{ReasonInvalidDeadline}, Details: []Detail{{Code: ReasonInvalidDeadline, Field: "deadline", RuleValue: strconv.Itoa(*rule.DeadlineWithinDays), NoticeValue: ""}}}
		}
		if notice.Deadline.Before(now) || notice.Deadline.Sub(now) > time.Duration(*rule.DeadlineWithinDays)*24*time.Hour {
			return Result{}
		}
		add(ReasonDeadlineWithinDays, "deadline", strconv.Itoa(*rule.DeadlineWithinDays))
	}
	result.Matched = true
	return result
}

func contains(value, term string) bool {
	return strings.Contains(cases.Fold().String(model.NormalizeText(value)), cases.Fold().String(model.NormalizeText(term)))
}

func hasTerms(terms []string) bool {
	for _, term := range terms {
		if strings.TrimSpace(term) != "" {
			return true
		}
	}
	return false
}

func matchingTerm(value string, terms []string) (string, bool) {
	for _, term := range terms {
		if strings.TrimSpace(term) != "" && contains(value, term) {
			return term, true
		}
	}
	return "", false
}

func keywordMatch(notice model.Notice, terms []string) (string, string, bool) {
	for _, term := range terms {
		if strings.TrimSpace(term) == "" {
			continue
		}
		for _, field := range []struct{ name, value string }{{"title", notice.Title}, {"agency", notice.Agency}, {"region", notice.Region}} {
			if contains(field.value, term) {
				return field.name, term, true
			}
		}
	}
	return "", "", false
}

func detailValue(notice model.Notice, field string) string {
	switch field {
	case "title":
		return notice.Title
	case "agency":
		return notice.Agency
	case "region":
		return notice.Region
	case "category":
		return string(notice.Category)
	case "amount":
		return strconv.FormatInt(notice.Amount, 10)
	case "deadline":
		return notice.Deadline.Format(time.RFC3339)
	}
	return ""
}

func weekdays(values []time.Weekday) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = v.String()
	}
	return out
}

func hasCategory(want model.Category, categories []model.Category) bool {
	for _, category := range categories {
		if want == category {
			return true
		}
	}
	return false
}

func hasWeekday(want time.Weekday, weekdays []time.Weekday) bool {
	for _, weekday := range weekdays {
		if want == weekday {
			return true
		}
	}
	return false
}
