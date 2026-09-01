# Task 3 report — Web interface

## Result

Implemented the embeddable, server-rendered UI for login, dashboard, notices,
notice detail, filters, notifications, tenant settings, and platform admin.
All displayed records and counts are labeled sample data where applicable.

## Files

- `internal/web/handler.go`
- `internal/web/handler_test.go`
- `web/embed.go`
- `web/templates/base.html`
- `web/templates/pages.html`
- `web/static/app.css`
- `web/static/app.js`

## TDD evidence

- RED: `go test ./internal/web` failed with `undefined: NewHandler` before the
  handler and templates existed.
- First GREEN attempt exposed an over-specific login landmark assertion; it was
  narrowed from an exact tag to the landmark/class contract.
- GREEN: `go test ./internal/web` passed after implementation.
- Tests cover every route, semantic markers, the approved dashboard flow,
  notice empty/error/loading states, embedded CSS/JS, login redirect, 404, and
  unsupported methods.

## Visual and responsive evidence

- In-app Chromium inspection reproduced the angled navy rule, four connected
  process cards, green automation line, two cyan expansion panels, and restrained
  navy/cyan/gray/green palette.
- Viewport checks: 1440x900, 1024x768, 981x714, and 390x844 had no horizontal
  document overflow.
- At 390px the sidebar collapses, process cards become one column, and notice
  table rows become labeled blocks.
- The Impeccable detector returned no findings, but used degraded regex mode
  because its optional HTML/CSS parser modules were unavailable; computed
  selector and contrast checks were therefore not available from the detector.

## Assumptions

- `internal/web` local view models are integration seams, not domain models.
- The demo handler never persists or redirects to a fake saved state. Production
  POST actions require injected authorization context, CSRF, validation, and
  `Actions`; real persistence remains a Task 4 integration responsibility.
- Buttons that require unavailable integration are visibly disabled instead of
  implying a working action.

## Concerns

- No live OpenAPI, SMTP, PostgreSQL, authenticated session, or FreeBSD runtime
  was used in this task.
- The first automated mobile-menu click check hit a browser selector timeout;
  responsive rendering and collapsed state were verified visually, while the
  JavaScript behavior remains covered only by asset delivery and manual code
  inspection in this track.

## Fix round 1

### Changes

- Mobile navigation now initializes closed as `inert` and `aria-hidden`, with
  the menu button before the drawer in logical order. Opening focuses the first
  service link; Escape closes the drawer and restores button focus. No focus
  trap was added.
- Notice search, category, and region controls filter the local sample rows and
  preserve selected values. Filter switches submit an explicit sample-state
  POST action. Integration-only controls are disabled and linked to visible
  explanations with `aria-describedby`.
- Settings and admin tables now use scoped column headers and mobile
  `data-label` cells. The process flow is an ordered list with ordered headings.
- Detail lookup accepts only known, single-segment sample IDs. Empty match
  reasons and empty or multibyte names are handled safely.
- Route-specific `Allow` values, 44px control targets, a persistent input focus
  outline, and a non-animated reduced-motion loading state were added.
- Text cyan `#006B98` and focus `#006A9D` measure 5.90:1 and 5.92:1 against
  white respectively.

### TDD evidence

- RED: `go test -count=1 ./internal/web` failed to compile with
  `undefined: firstReason` and `undefined: initial` after the new regression
  tests were added and before production changes.
- GREEN: `go test -count=1 ./internal/web` passed after the route, view-model,
  template, CSS, and JavaScript changes.
- Added regression coverage for drawer source order/ARIA contract, ordered
  process stages, GET filtering, escaped query values, valid and invalid notice
  IDs, filter-toggle POST state, table labels, disabled-control explanations,
  Unicode/empty helpers, and route-aware `Allow` headers.

### Remaining concern

- Focus transfer and accessibility-tree behavior require the controller's
  browser QA; this fix round intentionally makes no browser-verification claim.

## Fix round 2

### Integration seam

- Added `NewHandlerWithOptions(Options) (http.Handler, error)` for production.
  It requires a `Backend`, `Actions`, and `RequestContextMapper`.
- `RequestContext` supplies the authenticated user, tenant, role, and CSRF
  token. `AppData` supplies dashboard, notices, filters, recipients, members,
  and tenants through one tenant-aware read snapshot.
- `Actions` receives validated tenant and platform commands only after
  constant-time CSRF comparison and capability checks succeed.
- The `platform_admin` role controls platform navigation and `/admin` access.
  Authentication and login interception remain responsibilities of the outer
  application middleware.
- `NewHandler()` is now explicitly a read-only sample handler for browser QA.
  Its login and mutation controls are disabled with visible explanations; a
  direct POST returns `501 Not Implemented` and never redirects to fake success.

### Accessibility follow-up

- Sidebar focus uses a white scoped outline on navy (13.72:1).
- Placeholder text is `#5F6B7A` on white (5.43:1).
- Switch labels and standalone action links have at least a 44x44px hit area.

### TDD evidence

- RED 1: `go test -count=1 ./internal/web` failed before implementation with
  undefined production contracts including `AppData`, `RequestContext`,
  `FilterCommand`, and `ToggleFilterCommand`.
- The first implementation run then exposed an incorrect `/login` `Allow`
  value (`GET, HEAD, POST` instead of `GET, HEAD`); correcting the route made
  the integration suite GREEN.
- RED 2: the added authorization/validation tests observed `/admin` returning
  `200` for a member and an arbitrary category returning `303` while calling
  the action.
- GREEN 2: `go test -count=1 ./internal/web` passed after role enforcement and
  category/region allow-list validation.
- Coverage now verifies read-only demo behavior, injected data/context mapping,
  production CSRF fields, action invocation, invalid CSRF and command rejection,
  required production dependencies, and platform-role authorization.

### Remaining concerns

- Root integration must implement the tenant-aware `Backend` snapshot and all
  `Actions`, and must wrap this handler with authentication/session middleware.
- Browser behavior and visual regression remain for the controller's QA; this
  round makes no browser-verification claim.

## Fix round 3 — final integration contract

### Final contract

- `RequestContext` now includes `TenantID`. Members are read-only;
  `tenant_admin` and `platform_admin` can mutate tenant settings only when a
  tenant is selected. `platform_admin` can read `/admin` without a tenant, but
  cannot mutate tenant state without `TenantID`.
- Authorization runs before every mutation Action. Non-platform `/admin`
  requests are rejected before `Backend.Load`.
- `Backend.Load` receives `PageRequest{Path: ...}` so integration can load only
  the requested read model.
- `AppData` now carries delivery time, timezone, contact email, admin health and
  counts, and an explicit demo flag. `NoticeView` carries a validated HTTP(S)
  source URL. Production pages no longer render sample badges or hardcoded
  schedule, contact, or admin values.
- Production login renders an enabled CSRF-bearing form for outer auth
  middleware interception. The demo login remains disabled and read-only; the
  handler itself still does not authenticate arbitrary credentials.
- `FilterCommand.MinimumAmount` is an optional `*int64`; blank is `nil`, and
  malformed or negative input is rejected before Actions.
- Both filter-form “전체” options submit `value=""`. Read-only roles receive
  disabled mutation fields and controls.
- Mobile notice primary links, switches, and standalone action links have a
  minimum 44px target.

### TDD evidence

- RED: `go test -count=1 ./internal/web` failed before implementation on the
  old string `MinimumAmount`, missing `TenantID` and `PageRequest`, and missing
  `SourceURL`, schedule/contact/admin/demo view fields.
- GREEN: `go test -count=1 ./internal/web` passed after capability enforcement,
  page-aware Backend loading, numeric validation, and dynamic template mapping.
- Added coverage for empty “전체” values, default saves, member/tenant/platform
  capabilities, pre-load admin rejection, enabled production login, sample-label
  suppression, every new view field, safe source URL, notification/settings
  mapping and validation, Action failures, and optional numeric amounts.

### Remaining concerns

- Root integration must map its authenticated session and domain records into
  the final public web contracts and intercept production `/login` before this
  handler.
- Browser and accessibility regression QA remains with the controller; this
  round makes no browser-verification claim.

## Final independent-review UI contracts

### Changes

- `AppData.DeliveryDays` and `NotificationCommand.DeliveryDays` use weekday
  values `0` through `6`. The form renders Sunday through Saturday checkboxes;
  at least one valid day is required and duplicates are normalized.
- Added `RecipientCommand` and `Actions.AddRecipient`. Tenant-capable admins can
  add a validated name/email recipient through `/notifications/recipients`.
- Added platform-only `Actions.RunCollection` and `Actions.SendTestMail` through
  `/admin/collect` and `/admin/test-mail`. Both require CSRF, surface action
  errors, redirect after success, and display a status result on `/admin`.
- Production platform buttons are active. Demo and unauthorized mutation
  controls remain disabled, and direct requests return `501` or `403` without
  invoking Actions.

### TDD evidence

- RED: `go test -count=1 ./internal/web` failed before implementation because
  `NotificationCommand.DeliveryDays`, `RecipientCommand`, and
  `AppData.DeliveryDays` did not exist.
- GREEN: `go test -count=1 ./internal/web` passed after the contracts, routes,
  validation, forms, results, and permission handling were implemented.
- Tests cover weekday mapping and bounds, zero-day rejection, recipient
  validation/CSRF/Action errors, platform CSRF/permissions/Action errors,
  demo/member rejection, and successful result messages.

### Remaining concern

- Root integration must implement the three new Actions and persist delivery
  weekdays. Browser QA is intentionally excluded from this fix.
