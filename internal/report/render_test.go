package report

import (
	"strings"
	"testing"
	"time"
)

func TestBuildHTMLRendersEscapedDeterministicReport(t *testing.T) {
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	doc := Document{
		TenantName:   "<고객 & 파트너>",
		ScheduleName: "매일 <오전>",
		Trigger:      "일일 & 수동",
		DueAt:        time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC),
		WindowStart:  &start,
		WindowEnd:    time.Date(2026, 9, 2, 8, 59, 0, 0, time.UTC),
		Notices: []Notice{
			{Title: "첫 공고 <A>", Category: "service", Agency: "기관 & 본부", Region: "서울", Amount: 1200000, Deadline: time.Date(2026, 9, 8, 18, 0, 0, 0, time.UTC), SourceURL: "https://example.test/notices/1?a=1&b=2", Matches: []Match{{RuleName: "금액 <조건>", Reasons: []string{"금액 100만원 이상", "서울 & 인근"}}, {RuleName: "업종 <조건>", Reasons: []string{"전문 & 면허", "직접 <수행>"}}}},
			{Title: "둘째 공고", Category: "construction", SourceURL: "http://example.test/notices/2?kind=construction"},
			{Title: "셋째 공고", Category: "goods"},
			{Title: "넷째 공고", Category: "foreign"},
			{Title: "다섯째 공고", Category: "기타 <분류>", Region: "부산 & <남부>"},
		},
	}

	first, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("same document rendered different HTML")
	}

	html := string(first)
	for _, want := range []string{
		"<!doctype html>", "<html lang=\"ko\">", "&lt;고객 &amp; 파트너&gt;", "매일 &lt;오전&gt;", "일일 &amp; 수동",
		"2026-09-01 09:00 UTC ~ 2026-09-02 08:59 UTC", "2026-09-02 09:00 UTC", "총 5건",
		"공사 1", "용역 1", "물품 1", "외자 1", "첫 공고 &lt;A&gt;", "기관 &amp; 본부", "금액 &lt;조건&gt;", "서울 &amp; 인근", "업종 &lt;조건&gt;", "전문 &amp; 면허", "직접 &lt;수행&gt;",
		"1,200,000원", "2026-09-08 18:00 UTC", "href=\"https://example.test/notices/1?a=1&amp;b=2\"", "href=\"http://example.test/notices/2?kind=construction\"", "둘째 공고", "셋째 공고", "넷째 공고", "기타 &lt;분류&gt;", "부산 &amp; &lt;남부&gt;", "미정", "일치 규칙 없음",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<고객 & 파트너>") || strings.Contains(html, "첫 공고 <A>") {
		t.Fatalf("unescaped text leaked: %s", html)
	}
	assertInOrder(t, html, "공사 1", "용역 1", "물품 1", "외자 1")
	assertInOrder(t, html, "금액 &lt;조건&gt;", "금액 100만원 이상", "서울 &amp; 인근", "업종 &lt;조건&gt;", "전문 &amp; 면허", "직접 &lt;수행&gt;")
	assertInOrder(t, html, "첫 공고 &lt;A&gt;", "둘째 공고", "셋째 공고", "넷째 공고", "다섯째 공고")
}

func TestBuildHTMLShowsInitialCollectionAndRejectsUnsafeLinks(t *testing.T) {
	doc := Document{
		TenantName:   "테넌트",
		ScheduleName: "수집",
		WindowEnd:    time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
		Notices: []Notice{
			{Title: "스크립트 URL", SourceURL: "javascript:alert(1)"},
			{Title: "호스트 없는 URL", SourceURL: "https:///missing-host"},
		},
	}

	html, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{"최초 수집 이후", "2026-09-02 10:00 UTC", "javascript:alert(1)", "https:///missing-host", "마감 미정"} {
		if !strings.Contains(got, want) {
			t.Fatalf("HTML missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"href=\"javascript:alert(1)\"", "href=\"https:///missing-host\"", "#ZgotmplZ"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("unsafe link leaked %q:\n%s", unwanted, got)
		}
	}
}

func TestBuildHTMLHandlesNoNotices(t *testing.T) {
	html, err := BuildHTML(Document{TenantName: "테넌트", ScheduleName: "일정"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "새로 일치한 공고가 없습니다.") || !strings.Contains(string(html), "총 0건") {
		t.Fatalf("empty report is incomplete: %s", html)
	}
}

func TestBuildHTMLRendersSearchHistoryTable(t *testing.T) {
	collected := time.Date(2026, 1, 7, 14, 54, 0, 0, time.UTC) // KST 23:54
	posted := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)      // KST 10:00
	recorded := time.Date(2026, 1, 7, 14, 55, 12, 0, time.UTC) // KST 23:55:12
	doc := Document{
		TenantName: "테넌트", ScheduleName: "일정", Trigger: "수동",
		DueAt:     time.Date(2026, 1, 7, 14, 55, 0, 0, time.UTC),
		WindowEnd: time.Date(2026, 1, 7, 14, 55, 0, 0, time.UTC),
		Notices: []Notice{
			{
				Title: "스마트폴 <제작>", Category: "goods", SourceKind: "입찰공고목록-입찰공고",
				Agency: "만원복지재단 & 부설", Keywords: []string{"경관조명", "스마트폴"},
				Amount: 599995000, PostedAt: posted, CollectedAt: collected, RecordedAt: recorded,
				Matches: []Match{{RuleName: "경관조명", Reasons: []string{"포함 키워드 일치"}}},
			},
			{Title: "열 없는 공고", Category: "service", Amount: 0},
		},
	}

	html, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{
		"<table", "검색내역 조회",
		">#<", ">구분<", ">일자<", ">시간<", ">키워드<", ">업무구분<", ">업무여부<",
		">사업명/공고명<", ">진행/게시일자<", ">레코드생성일시<",
		"입찰공고목록-입찰공고", "20260107", "2354", "경관조명, 스마트폴",
		"내자", "스마트폴 &lt;제작&gt;", "만원복지재단 &amp; 부설",
		"2026-01-02", "599,995,000원", "2026-01-07 23:55:12", "2 rows",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
	// 값이 없는 셀은 추정하지 않고 비워 둔다.
	if !strings.Contains(got, ">-<") {
		t.Fatalf("missing values are not rendered as a dash:\n%s", got)
	}
	// 표의 행 순서가 곧 # 순서다.
	assertInOrder(t, got, "스마트폴 &lt;제작&gt;", "열 없는 공고")
}

func TestReportStyleSupportsWideTable(t *testing.T) {
	html, err := BuildHTML(Document{TenantName: "테넌트", ScheduleName: "일정",
		Notices: []Notice{{Title: "가", Category: "goods"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"overflow-x:auto", "size:A4 landscape", "border-collapse"} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("style missing %q", want)
		}
	}
}

func TestBuildHTMLShowsQueryCriteriaAndAmountTotal(t *testing.T) {
	doc := Document{
		TenantName: "테넌트", ScheduleName: "일정",
		DueAt:     time.Date(2026, 1, 7, 14, 55, 0, 0, time.UTC),
		WindowEnd: time.Date(2026, 1, 7, 14, 55, 0, 0, time.UTC),
		Notices: []Notice{
			{Title: "가", Category: "goods", SourceKind: "입찰공고목록-입찰공고", Keywords: []string{"경관조명"}, Amount: 100, Matches: []Match{{RuleName: "조명"}}},
			{Title: "나", Category: "goods", SourceKind: "입찰공고목록-입찰공고", Keywords: []string{"스마트폴"}, Amount: 250, Matches: []Match{{RuleName: "폴"}}},
		},
	}
	got, err := BuildHTML(doc)
	if err != nil {
		t.Fatal(err)
	}
	html := string(got)
	for _, want := range []string{
		"2026-01-07",  // 조회 일자(KST)
		"경관조명, 스마트폴",  // 조회 키워드 = 표에 나온 키워드 합집합
		"입찰공고목록-입찰공고", // 조회 구분
		"조명, 폴",       // 적용 필터
		"350원",        // 추정가격 합계
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("query criteria missing %q:\n%s", want, html)
		}
	}
}

func TestBuildHTMLKeepsForeignProcurementLabel(t *testing.T) {
	got, err := BuildHTML(Document{
		TenantName: "테넌트", ScheduleName: "일정",
		Notices: []Notice{{Title: "외자 건", Category: "foreign"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "외자") {
		t.Fatalf("foreign notice is not labelled 외자:\n%s", got)
	}
}

func assertInOrder(t *testing.T, value string, parts ...string) {
	t.Helper()
	offset := 0
	for _, part := range parts {
		next := strings.Index(value[offset:], part)
		if next == -1 {
			t.Fatalf("%q is not after the prior value in %s", part, value)
		}
		offset += next + len(part)
	}
}
