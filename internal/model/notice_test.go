package model

import (
	"testing"
	"time"
)

func TestNoticeIdentityIsStableForSameOfficialKey(t *testing.T) {
	first := Notice{Category: CategoryConstruction, BidNumber: "20260901001", BidSequence: "00", Title: "최초 공고", Agency: "기관 A", Amount: 100}
	second := Notice{Category: CategoryConstruction, BidNumber: "20260901001", BidSequence: "00", Title: "변경 공고", Agency: "기관 B", Amount: 200}
	if first.Identity() == "" || second.Identity() == "" {
		t.Fatal("valid notices must have an identity")
	}
	if first.Identity() != second.Identity() {
		t.Fatalf("identity changed: %q != %q", first.Identity(), second.Identity())
	}
}

func TestNoticeRevisionChangesWhenNoticeContentChanges(t *testing.T) {
	deadline := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	base := Notice{Category: CategoryGoods, BidNumber: "20260901002", BidSequence: "01", Title: "  도로 보수 자재  ", Agency: "조달청", Deadline: deadline, Amount: 1000}
	changed := base
	changed.Amount = 1001
	if base.Revision() == changed.Revision() {
		t.Fatal("revision must change when the amount changes")
	}
}

func TestNormalizeTextUsesUnicodeNFCAndTrimsSpace(t *testing.T) {
	if got, want := NormalizeText("  cafe\u0301  "), "café"; got != want {
		t.Fatalf("NormalizeText() = %q, want %q", got, want)
	}
}

func TestInvalidSourceNoticeHasNoIdentityOrRevision(t *testing.T) {
	notice := Notice{Category: CategoryGoods, BidNumber: "", BidSequence: "00", Title: "예시 물품"}
	if notice.Identity() != "" || notice.Revision() != "" {
		t.Fatalf("invalid source notice must not be hashed: %+v", notice)
	}
	if err := notice.ValidateSource(); err == nil {
		t.Fatal("missing bid number must be rejected")
	}
}

func TestUnknownCategoryHasNoSourceIdentity(t *testing.T) {
	notice := Notice{Category: "unknown", BidNumber: "N", BidSequence: "00", Title: "title"}
	if notice.Identity() != "" || notice.ValidateSource() == nil {
		t.Fatal("unknown category must be invalid source identity")
	}
}

func TestNoticePreservesOfficialSourceURLInRevision(t *testing.T) {
	notice := Notice{Category: CategoryGoods, BidNumber: "N", BidSequence: "00", Title: "title", SourceURL: "https://www.g2b.go.kr/detail?bid=N"}
	if notice.SourceURL == "" || notice.Revision() == "" {
		t.Fatal("official detail URL must be preserved")
	}
}
