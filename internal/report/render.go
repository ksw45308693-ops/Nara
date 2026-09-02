// Package report renders deterministic standalone HTML procurement reports.
package report

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Match struct {
	RuleName string
	Reasons  []string
}

type Notice struct {
	ID, Title, Category, Agency, Region, SourceURL string
	Amount                                         int64
	Deadline                                       time.Time
	Matches                                        []Match
}

type Document struct {
	TenantName, ScheduleName string
	Trigger                  string
	DueAt, WindowEnd         time.Time
	WindowStart              *time.Time
	Notices                  []Notice
}

type reportView struct {
	TenantName, ScheduleName, Trigger string
	Window, DueAt                     string
	Total                             int
	Construction, Service             int
	Goods, Foreign                    int
	Notices                           []noticeView
}

type noticeView struct {
	ID, Title, Category, Agency, Region, SourceURL string
	Amount, Deadline                               string
	Link                                           template.URL
	HasLink                                        bool
	Matches                                        []matchView
}

type matchView struct {
	RuleName string
	Reasons  []string
}

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>입찰공고 보고서</title><style>
body{margin:0;background:#f3f6f8;color:#162331;font:14px/1.55 Arial,sans-serif}.report{max-width:880px;margin:0 auto;padding:28px}.header{background:#092a4a;color:#fff;padding:24px;border-radius:10px}.header h1{margin:0;font-size:24px}.header p{margin:5px 0 0;color:#bcecff}.meta,.summary,.notice{background:#fff;border:1px solid #d9e2e8;border-radius:8px;margin-top:16px;padding:18px}.meta{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.label{color:#527080;font-size:12px}.value{font-weight:600}.counts{display:flex;flex-wrap:wrap;gap:8px}.count{background:#e6f7f7;color:#0b5d68;border-radius:999px;padding:4px 10px}.notice h2{font-size:18px;margin:0 0 8px}.notice dl{display:grid;grid-template-columns:100px 1fr;gap:5px 12px;margin:0}.notice dt{color:#527080}.notice dd{margin:0}.matches{margin:12px 0 0;padding-left:18px}.matches li{margin:5px 0}.source{overflow-wrap:anywhere}.empty{color:#527080}@media print{body{background:#fff}.report{max-width:none;padding:0}.header,.meta,.summary,.notice{break-inside:avoid}}
</style></head><body><main class="report"><header class="header"><h1>입찰공고 보고서</h1><p>{{.TenantName}} · {{.ScheduleName}}</p></header>
<section class="meta"><div><div class="label">실행 구분</div><div class="value">{{.Trigger}}</div></div><div><div class="label">일치 조회 기간</div><div class="value">{{.Window}}</div></div><div><div class="label">기준 시각</div><div class="value">{{.DueAt}}</div></div><div><div class="label">일치 공고</div><div class="value">총 {{.Total}}건</div></div></section>
<section class="summary"><strong>분류별 현황</strong><div class="counts"><span class="count">공사 {{.Construction}}</span><span class="count">용역 {{.Service}}</span><span class="count">물품 {{.Goods}}</span><span class="count">외자 {{.Foreign}}</span></div></section>
{{if .Notices}}{{range .Notices}}<article class="notice"><h2>{{.Title}}</h2><dl><dt>분류</dt><dd>{{.Category}}</dd><dt>기관</dt><dd>{{.Agency}}</dd><dt>지역</dt><dd>{{.Region}}</dd><dt>추정 금액</dt><dd>{{.Amount}}</dd><dt>마감</dt><dd>{{.Deadline}}</dd><dt>원문</dt><dd class="source">{{if .HasLink}}<a href="{{.Link}}">{{.SourceURL}}</a>{{else}}{{.SourceURL}}{{end}}</dd></dl>{{if .Matches}}<ul class="matches">{{range .Matches}}<li><strong>{{.RuleName}}</strong>{{if .Reasons}}<ul>{{range .Reasons}}<li>{{.}}</li>{{end}}</ul>{{end}}</li>{{end}}</ul>{{else}}<p class="empty">일치 규칙 없음</p>{{end}}</article>{{end}}{{else}}<section class="summary empty">새로 일치한 공고가 없습니다.</section>{{end}}
</main></body></html>`))

// BuildHTML returns a complete self-contained Korean HTML report.
func BuildHTML(doc Document) ([]byte, error) {
	view := reportView{
		TenantName:   doc.TenantName,
		ScheduleName: doc.ScheduleName,
		Trigger:      doc.Trigger,
		DueAt:        formatTime(doc.DueAt),
		Window:       formatWindow(doc.WindowStart, doc.WindowEnd),
		Total:        len(doc.Notices),
		Notices:      make([]noticeView, 0, len(doc.Notices)),
	}
	for _, notice := range doc.Notices {
		countCategory(&view, notice.Category)
		entry := noticeView{
			ID: notice.ID, Title: notice.Title, Category: categoryLabel(notice.Category), Agency: notice.Agency, Region: notice.Region,
			SourceURL: notice.SourceURL, Amount: formatAmount(notice.Amount), Deadline: formatDeadline(notice.Deadline),
			Matches: make([]matchView, 0, len(notice.Matches)),
		}
		if validSourceURL(notice.SourceURL) {
			entry.HasLink = true
			entry.Link = template.URL(notice.SourceURL)
		}
		for _, match := range notice.Matches {
			entry.Matches = append(entry.Matches, matchView{RuleName: match.RuleName, Reasons: match.Reasons})
		}
		view.Notices = append(view.Notices, entry)
	}
	var output bytes.Buffer
	if err := reportTemplate.Execute(&output, view); err != nil {
		return nil, fmt.Errorf("render report: %w", err)
	}
	return output.Bytes(), nil
}

func countCategory(view *reportView, category string) {
	switch category {
	case "construction":
		view.Construction++
	case "service":
		view.Service++
	case "goods":
		view.Goods++
	case "foreign":
		view.Foreign++
	}
}

func categoryLabel(category string) string {
	switch category {
	case "construction":
		return "공사"
	case "service":
		return "용역"
	case "goods":
		return "물품"
	case "foreign":
		return "외자"
	default:
		return category
	}
}

func formatWindow(start *time.Time, end time.Time) string {
	if start == nil {
		return "최초 수집 이후 ~ " + formatTime(end)
	}
	return formatTime(*start) + " ~ " + formatTime(end)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("2006-01-02 15:04 MST")
}

func formatDeadline(value time.Time) string {
	if value.IsZero() {
		return "마감 미정"
	}
	return formatTime(value)
}

func formatAmount(value int64) string {
	if value == 0 {
		return "미정"
	}
	text := strconv.FormatInt(value, 10)
	prefix := ""
	if strings.HasPrefix(text, "-") {
		prefix, text = "-", text[1:]
	}
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	return prefix + text + "원"
}

func validSourceURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.IsAbs() && parsed.Host != "" && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https"))
}
