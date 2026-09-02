# Task 1 report — procurement and matching

## Files changed

- `internal/model/notice.go`, `internal/model/notice_test.go`
- `internal/procurement/client.go`, `internal/procurement/client_test.go`
- `internal/procurement/testdata/sanitized-construction-page.json`
- `internal/matcher/matcher.go`, `internal/matcher/matcher_test.go`

## Delivered behavior

- Normalized `Notice` records with category, stable official-key identity hash,
  revision hash, and NFC text normalization.
- JSON client for construction, service, goods, and foreign operations.
  It paginates, parses public-service errors, and retries transport/429/5xx
  failures with capped exponential backoff.
- Unicode-aware include-any/include-all/exclude matching, plus category,
  agency, region, amount, deadline weekday, and deadline-window rules.
  Successful matches include explicit reason codes.
- All API tests use `httptest`; the checked-in JSON is anonymized fixture data.

## RED evidence

| Command | Expected RED output |
| --- | --- |
| `go test ./internal/model` | `undefined: Notice`, `CategoryConstruction`, and `NormalizeText` |
| `go test ./internal/matcher` | `undefined: Match`, `Rule`, and `ReasonIncludeAny` |
| `go test ./internal/matcher` | unknown `Rule` filter fields and missing rule reason constants |
| `go test ./internal/procurement` | `undefined: NewClient`, `Config`, `ListQuery`, and `ServiceError` |

## GREEN evidence

| Command | Output |
| --- | --- |
| `go test ./internal/model` | `ok namo/internal/model` |
| `go test ./internal/matcher` | `ok namo/internal/matcher` |
| `go test ./internal/procurement` | `ok namo/internal/procurement` |
| `go test -count=1 ./internal/model ./internal/procurement ./internal/matcher` | all three packages `ok` |
| `go vet ./internal/model ./internal/procurement ./internal/matcher` | exit 0, no output |

## Tests

- Identity stability, revision changes, and NFC text normalization.
- Unicode include-any, exclude reason, and all practical filter reasons.
- Construction operation query, two-page JSON parsing, service error decoding,
  bounded retry, and all four operation mappings.

## Assumptions and concerns

- The API base URL is supplied through `Config.BaseURL`; its service-specific
  operation path is appended by the client.
- The foreign operation path is mapped as
  `getFrgcptBidPblancListInfoFrgcpt`; confirm it against the deployed portal's
  current service registration before enabling live collection.
- No live API key was available, so live credentials, rate limits, and portal
  payload variants remain unverified.

## Review fix round 1

### Corrections

- Set the official service base default and corrected every operation to the
  `getBidPblancListInfo*` contract. Requests now send `inqryDiv=1` and Korean
  hourly `YYYYMMDDHHMM` bounds.
- `ServiceKey` is explicitly decoded input, URL-encoded once by the client,
  and pre-encoded `%` input is rejected. The default HTTP timeout is 20s;
  response reads are capped at 8 MiB and page size at 1,000.
- Added context-aware retry waits, `Retry-After`, transient service codes
  `01`, `05`, and `23`, malformed-envelope rejection, and empty-page guard.
- Removed `rgstTyNm` region mapping. Only `prtcptPsblRgnNm` is used; absent
  region data is blank with a typed warning. Invalid records are warned and
  skipped independently, never given an identity or revision hash.
- Match results retain stable reason codes and now include field/rule detail.
  Keyword checks cover normalized title, agency, and valid region; zero
  deadlines reject deadline rules explicitly.

### RED evidence

| Command | Expected RED output |
| --- | --- |
| `go test ./internal/model -run TestInvalidSourceNoticeHasNoIdentityOrRevision -v` | `notice.ValidateSource undefined` |
| `go test ./internal/procurement` | undefined official-base, warning, cap, and malformed-response symbols |
| `go test ./internal/matcher` | missing `Result.Details` and `ReasonInvalidDeadline` |

### GREEN evidence

| Command | Output |
| --- | --- |
| `go test ./internal/procurement` | `ok namo/internal/procurement` |
| `go test ./internal/matcher` | `ok namo/internal/matcher` |
| `go test -count=1 ./internal/model ./internal/procurement ./internal/matcher` | all three packages `ok` |
| `go vet ./internal/model ./internal/procurement ./internal/matcher` | exit 0, no output |

### Remaining concern

The contract is unit-tested against sanitized JSON only. Validate the exact
portal service base and all category payload variants with an approved live
decoded key before enabling collection.

## Review fix round 2

- Official base is now `https://apis.data.go.kr/1230000/ad/BidPublicInfoService`
  with an independent literal test. `totalCount` accepts string or number.
- Added cached `LookupRegion` for `getBidPblancListInfoPrtcptPsblRgn`; invoke
  it only for new/revised notices because each cache miss consumes API quota.
- Source validation requires a supported category. Match details now carry
  rule and notice values for keywords and practical filters.

### RED / GREEN evidence

| Stage | Command | Result |
| --- | --- | --- |
| RED | `go test ./internal/model -run TestUnknownCategoryHasNoSourceIdentity -v` | rejected unknown category expectation failed before validation |
| RED | `go test ./internal/procurement -run 'TestOfficialBaseURLLiteral|TestListReadsStringTotalCountAndRegionLookupCaches' -v` | `LookupRegion undefined` |
| GREEN | `go test ./internal/model ./internal/procurement ./internal/matcher` | all packages `ok` |

Live Swagger validation with an approved decoded key remains required for the
participant-region query parameter and category-specific payload variations.

### Notice detail link

`model.Notice.SourceURL` preserves only the official `bidNtceDtlUrl` response
field, and it participates in revision hashing. No deterministic URL is
guessed when that field is absent. RED: the source-URL model test initially
failed with an unknown field; GREEN: scoped tests and `go vet` passed.

## Review fix round 3

- Region lookup now keys its finite cache and safety budget by notice number
  plus sequence, sends the documented paging/filter query fields, and returns
  deterministically combined participant-region values. Empty results cache.
- `flexInt` accepts only non-negative integral string/number values and rejects
  fractions and overflow. Notice identity tests now use valid source records.
- RED: `TestFlexIntRejectsFractionsAndNegativeValues` initially accepted `1.5`;
  GREEN: scoped `go test ./internal/model ./internal/procurement ./internal/matcher`
  and `go vet` passed.

Remaining concern: concurrent-miss coalescing, page error sentinels, and live
Swagger validation still need a dedicated final integration pass.

## Review fix round 4 — final scoped pass

- `LookupRegion` now uses the same bounded request path as `List`, including
  decoded-key validation, HTTP/status and transient-envelope retry, capped
  `Retry-After`, response-size checks, envelope validation, and nil-body
  rejection. It sends `pageNo=1`, bounded `numOfRows`, `inqryDiv=1`,
  `bidNtceNo`, and `bidNtceOrd`.
- Region results are normalized, deduplicated, sorted, and cached even when
  empty. The finite 500-entry cache key includes notice number and sequence.
  Concurrent misses for one key share one in-flight request without an added
  dependency.
- The per-client lookup budget defaults to 500. Exhaustion returns typed
  `LookupBudgetError` and remains compatible with `errors.Is(err,
  ErrLookupBudget)`.
- Pagination has a configurable finite `MaxPages` guard and typed
  `IncompletePageError`, `RepeatedPageError`, and `MaxPageError`. Every page
  failure returns a nil notice slice instead of partial success.
- Matcher details now keep the matched rule term in `RuleValue` and the notice
  field in `NoticeValue` for exclude, agency, and region rules. Invalid
  deadlines include the active deadline rule value and a blank unavailable
  notice value.
- The identity stability test now compares valid notices with the same
  official key but different revision content. `flexInt` raw parsing rejects
  negative, fractional, and overflowing numeric and string values. The
  official `/ad/BidPublicInfoService` base and `bidNtceDtlUrl` `SourceURL`
  mapping remain unchanged.

### RED evidence

| Command | Observed RED result |
| --- | --- |
| `go test -count=1 ./internal/matcher -run 'TestMatchKeywordsInspectAgencyAndRegionWithDetails|TestMatchAgencyAndRegionDetailsKeepRuleTermsAndNoticeValues|TestMatchInvalidDeadlineDetailsKeepApplicableRuleValues' -v` | exclude `NoticeValue` was blank; agency `RuleValue` held the notice agency; invalid-deadline rule values were blank |
| `go test -count=1 ./internal/procurement -run 'TestLookupRegionCoalescesConcurrentMisses|TestLookupRegionRetriesHTTPStatusAndClampsRetryAfter|TestLookupRegionRejectsEncodedServiceKey' -v` | 8 transport attempts; 503 response decoded as invalid JSON; encoded key reached transport |
| `go test -count=1 ./internal/procurement -run TestLookupRegionRejectsNilEnvelopeBody -v` | nil-pointer panic in `(*apiBody).items` |
| `go test -count=1 ./internal/procurement -run 'TestLookupRegionBudgetIsTypedCachedAndPerClient|TestListReturnsTyped' -v` | undefined typed errors and missing `Config.MaxPages` |

### Final verification

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/model ./internal/procurement ./internal/matcher` | all three packages `ok` |
| `go vet ./internal/model ./internal/procurement ./internal/matcher` | exit 0, no output |

### Superseded concerns

The round 3 concern about coalescing and page guards is resolved by this pass.
The initial foreign-operation concern naming
`getFrgcptBidPblancListInfoFrgcpt` is also historical; the tested mapping is
`getBidPblancListInfoFrgcpt`. The only remaining external check is approved
live Swagger/API validation with a decoded key; no live credential or server
call was used in this work.
