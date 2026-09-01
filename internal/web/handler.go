// Package web provides the HTTP surface for the application UI.
package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	ui "g2b-monitor/web"
)

type pageData struct {
	Title         string
	Active        string
	State         string
	Saved         bool
	UserName      string
	TenantName    string
	Notices       []noticeView
	Notice        noticeView
	Filters       []filterView
	Recipients    []recipientView
	Members       []memberView
	Tenants       []tenantView
	CurrentDate   string
	SearchQuery   string
	Category      string
	Region        string
	Role          string
	CSRFToken     string
	Writable      bool
	Dashboard     DashboardView
	Demo          bool
	LoginEnabled  bool
	DeliveryTime  string
	Timezone      string
	ContactEmail  string
	Admin         AdminView
	DeliveryDays  []int
	AdminWritable bool
	AdminResult   string
	InviteResult  string
	Invitation    InvitationView
	InviteExpires string
	InviteToken   string
}

// RequestContext is the authenticated identity supplied by the outer app.
type RequestContext struct {
	UserID     string
	UserName   string
	Email      string
	TenantName string
	TenantID   string
	Role       string
	CSRFToken  string
}

// RequestContextMapper maps auth middleware state into the web view context.
type RequestContextMapper func(*http.Request) (RequestContext, error)

// DashboardView contains the observable collection summary.
type DashboardView struct {
	LastCollected string
	Collected     int
	NewNotices    int
	Matches       int
	ActiveFilters int
	RunTime       string
	NextDelivery  string
	Healthy       bool
}

// AdminView contains platform collection health.
type AdminView struct {
	Healthy        bool
	LastCollected  string
	CollectedCount int
	FailedJobs     int
}

// NoticeView is a notice row/detail prepared by integration.
type NoticeView struct {
	ID        string
	Title     string
	Category  string
	Agency    string
	Region    string
	Amount    string
	Deadline  string
	SourceURL string
	Reasons   []string
}

type noticeView = NoticeView

// FilterView is a tenant filter prepared by integration.
type FilterView struct {
	ID      string
	Name    string
	Summary string
	Matches int
	Enabled bool
}

type filterView = FilterView

// RecipientView is a digest recipient prepared by integration.
type RecipientView struct {
	Name  string
	Email string
	State string
}

type recipientView = RecipientView

// MemberView is a tenant member prepared by integration.
type MemberView struct {
	Name  string
	Email string
	Role  string
}

type memberView = MemberView

// TenantView is a platform tenant prepared by integration.
type TenantView struct {
	Name       string
	Members    int
	LastDigest string
	State      string
}

type tenantView = TenantView

// AppData is the read model loaded for server-rendered pages.
type AppData struct {
	Dashboard    DashboardView
	Notices      []NoticeView
	Filters      []FilterView
	Recipients   []RecipientView
	Members      []MemberView
	Tenants      []TenantView
	DeliveryTime string
	DeliveryDays []int
	Timezone     string
	ContactEmail string
	Admin        AdminView
	Demo         bool
}

// PageRequest identifies the read surface for selective backend loading.
type PageRequest struct {
	Path string
}

// Backend loads tenant-aware read models.
type Backend interface {
	Load(context.Context, RequestContext, PageRequest) (AppData, error)
}

// FilterCommand is a validated filter-save request.
type FilterCommand struct {
	Name            string
	IncludeKeywords string
	IncludeMode     string
	ExcludeKeywords string
	Category        string
	Region          string
	MinimumAmount   *int64
	DeadlineDays    int
	Agency          string
}

// ToggleFilterCommand is a validated filter-state request.
type ToggleFilterCommand struct {
	FilterID string
	Enabled  bool
}

// NotificationCommand is a validated digest schedule request.
type NotificationCommand struct {
	DeliveryTime string
	DeliveryDays []int
	Timezone     string
}

// RecipientCommand is a validated digest-recipient request.
type RecipientCommand struct {
	Name  string
	Email string
}

// SettingsCommand is a validated tenant settings request.
type SettingsCommand struct {
	TenantName   string
	ContactEmail string
}

var ErrInvitationUnavailable = errors.New("invitation is unavailable")
var ErrInvitationMailDelivery = errors.New("invitation was saved but mail delivery failed")

// TenantInviteCommand creates a tenant and its first administrator invite.
type TenantInviteCommand struct {
	TenantName, ContactEmail, AdminName, AdminEmail string
}

// MemberInviteCommand creates or replaces an invitation inside one tenant.
type MemberInviteCommand struct {
	Name, Email, Role string
}

type AcceptInviteCommand struct {
	Token, DisplayName, Password string
}

type InvitationView struct {
	TenantName, Email, DisplayName, Role string
	ExpiresAt                            time.Time
}

// Onboarding is the public and administrator invitation boundary.
type Onboarding interface {
	InviteTenant(context.Context, RequestContext, TenantInviteCommand) error
	InviteMember(context.Context, RequestContext, MemberInviteCommand) error
	Invitation(context.Context, string) (InvitationView, error)
	AcceptInvitation(context.Context, AcceptInviteCommand) error
}

// Actions applies validated tenant-aware mutation commands.
type Actions interface {
	SaveFilter(context.Context, RequestContext, FilterCommand) error
	ToggleFilter(context.Context, RequestContext, ToggleFilterCommand) error
	SaveNotification(context.Context, RequestContext, NotificationCommand) error
	SaveSettings(context.Context, RequestContext, SettingsCommand) error
	AddRecipient(context.Context, RequestContext, RecipientCommand) error
	RunCollection(context.Context, RequestContext) error
	SendTestMail(context.Context, RequestContext) error
}

// Options configures the production handler behind authentication middleware.
type Options struct {
	Backend    Backend
	Actions    Actions
	Onboarding Onboarding
	MapContext RequestContextMapper
}

// Handler serves the embeddable web interface. Domain integration can replace
// the local page models without changing template routes.
type Handler struct {
	templates  *template.Template
	assets     http.Handler
	backend    Backend
	actions    Actions
	onboarding Onboarding
	mapContext RequestContextMapper
	demo       bool
}

// NewHandler returns a read-only sample-data handler for browser QA.
func NewHandler() http.Handler {
	handler, err := newHandler(Options{
		Backend: sampleBackend{},
		MapContext: func(*http.Request) (RequestContext, error) {
			return RequestContext{UserName: "김담당", TenantName: "샘플 주식회사", Role: "platform_admin"}, nil
		},
	}, true)
	if err != nil {
		panic(err)
	}
	return handler
}

// NewHandlerWithOptions returns a production handler. Authentication remains
// an outer middleware responsibility; this handler never verifies credentials.
func NewHandlerWithOptions(options Options) (http.Handler, error) {
	if options.Backend == nil || options.Actions == nil || options.MapContext == nil {
		return nil, errors.New("web: Backend, Actions, and MapContext are required")
	}
	return newHandler(options, false)
}

func newHandler(options Options, demo bool) (http.Handler, error) {
	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"firstReason": firstReason,
		"initial":     initial,
		"safeURL":     safeURL,
		"number":      formatNumber,
		"hasDay":      hasDay,
		"weekdays":    weekdays,
	}).ParseFS(ui.Files, "templates/*.html"))
	assetsFS, err := fs.Sub(ui.Files, "static")
	if err != nil {
		return nil, err
	}
	return &Handler{
		templates:  templates,
		assets:     http.FileServer(http.FS(assetsFS)),
		backend:    options.Backend,
		actions:    options.Actions,
		onboarding: options.Onboarding,
		mapContext: options.MapContext,
		demo:       demo,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		h.serveAsset(w, r)
		return
	}
	requestContext, err := h.mapContext(r)
	if err != nil {
		http.Error(w, "사용자 정보를 확인하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin") && !canViewAdmin(requestContext) {
		http.Error(w, "플랫폼 관리자 권한이 필요합니다.", http.StatusForbidden)
		return
	}
	var appData AppData
	if allows(r.Method, http.MethodGet, http.MethodHead) && r.URL.Path != "/login" && r.URL.Path != "/accept-invite" && r.URL.Path != "/" {
		appData, err = h.backend.Load(r.Context(), requestContext, PageRequest{Path: r.URL.Path})
		if err != nil {
			http.Error(w, "화면 데이터를 불러오지 못했습니다.", http.StatusInternalServerError)
			return
		}
	}

	switch {
	case r.URL.Path == "/":
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	case r.URL.Path == "/login":
		if r.Method == http.MethodPost {
			if h.demo {
				demoNotImplemented(w)
				return
			}
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		data := page("로그인", "", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.LoginEnabled = !h.demo
		if !h.demo && r.URL.Query().Get("accepted") == "1" {
			data.InviteResult = "계정을 만들었습니다. 새 비밀번호로 로그인하세요."
		}
		h.render(w, "login", data)
	case r.URL.Path == "/accept-invite":
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if h.demo || h.onboarding == nil {
			h.renderStatus(w, http.StatusServiceUnavailable, "초대 기능을 사용할 수 없습니다", "관리자에게 문의해 주세요.")
			return
		}
		if r.Method == http.MethodPost {
			if r.FormValue("mode") == "inspect" {
				h.handleInspectInvitation(w, r, requestContext)
			} else {
				h.handleAcceptInvitation(w, r, requestContext)
			}
			return
		}
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead, http.MethodPost)
			return
		}
		if r.URL.RawQuery != "" {
			h.renderStatus(w, http.StatusBadRequest, "초대 링크를 확인해 주세요", "메일에 있는 최신 초대 링크를 다시 열어 주세요.")
			return
		}
		data := page("초대 수락", "", requestContext, false, false)
		h.render(w, "accept-invite-bootstrap", data)
	case r.URL.Path == "/dashboard":
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		data := page("입찰공고 모니터링", "dashboard", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Dashboard = appData.Dashboard
		h.render(w, "dashboard", data)
	case r.URL.Path == "/notices":
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		data := page("공고 목록", "notices", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.State = state(r)
		data.SearchQuery = strings.TrimSpace(r.URL.Query().Get("q"))
		data.Category = r.URL.Query().Get("category")
		data.Region = r.URL.Query().Get("region")
		data.Notices = filterNotices(appData.Notices, data.SearchQuery, data.Category, data.Region)
		if data.State == "empty" {
			data.Notices = nil
		}
		h.render(w, "notices", data)
	case strings.HasPrefix(r.URL.Path, "/notices/"):
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/notices/")
		notice, ok := findNotice(appData.Notices, id)
		if !ok {
			h.renderStatus(w, http.StatusNotFound, "찾을 수 없는 공고", "공고 번호를 확인하거나 목록으로 돌아가 주세요.")
			return
		}
		data := page("공고 상세", "notices", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Notice = notice
		h.render(w, "notice-detail", data)
	case r.URL.Path == "/filters":
		if r.Method == http.MethodPost {
			if h.demo {
				demoNotImplemented(w)
				return
			}
			if !canMutateTenant(requestContext) {
				http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
				return
			}
			h.handleSaveFilter(w, r, requestContext)
			return
		}
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead, http.MethodPost)
			return
		}
		data := page("필터 관리", "filters", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Saved = !h.demo && r.URL.Query().Get("saved") == "1"
		data.Filters = appData.Filters
		h.render(w, "filters", data)
	case r.URL.Path == "/filters/toggle":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		if !canMutateTenant(requestContext) {
			http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
			return
		}
		h.handleFilterToggle(w, r, requestContext)
	case r.URL.Path == "/notifications/recipients":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		if !canMutateTenant(requestContext) {
			http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
			return
		}
		h.handleAddRecipient(w, r, requestContext)
	case r.URL.Path == "/notifications":
		if r.Method == http.MethodPost {
			if h.demo {
				demoNotImplemented(w)
				return
			}
			if !canMutateTenant(requestContext) {
				http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
				return
			}
			h.handleSaveNotification(w, r, requestContext)
			return
		}
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead, http.MethodPost)
			return
		}
		data := page("알림 설정", "notifications", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Saved = !h.demo && r.URL.Query().Get("saved") == "1"
		data.Recipients = appData.Recipients
		data.DeliveryTime = appData.DeliveryTime
		data.DeliveryDays = appData.DeliveryDays
		data.Timezone = appData.Timezone
		data.Dashboard = appData.Dashboard
		h.render(w, "notifications", data)
	case r.URL.Path == "/settings":
		if r.Method == http.MethodPost {
			if h.demo {
				demoNotImplemented(w)
				return
			}
			if !canMutateTenant(requestContext) {
				http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
				return
			}
			h.handleSaveSettings(w, r, requestContext)
			return
		}
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead, http.MethodPost)
			return
		}
		data := page("환경 설정", "settings", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Saved = !h.demo && r.URL.Query().Get("saved") == "1"
		data.Members = appData.Members
		data.ContactEmail = appData.ContactEmail
		if r.URL.Query().Get("result") == "member-invited" {
			data.InviteResult = "구성원 초대를 보냈습니다."
		}
		h.render(w, "settings", data)
	case r.URL.Path == "/settings/invitations":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		if requestContext.Role != "tenant_admin" || requestContext.TenantID == "" {
			http.Error(w, "테넌트 관리자 권한이 필요합니다.", http.StatusForbidden)
			return
		}
		h.handleInviteMember(w, r, requestContext)
	case r.URL.Path == "/admin/tenants":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		h.handleInviteTenant(w, r, requestContext)
	case r.URL.Path == "/admin/collect":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		h.handlePlatformAction(w, r, requestContext, "collection")
	case r.URL.Path == "/admin/test-mail":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if h.demo {
			demoNotImplemented(w)
			return
		}
		h.handlePlatformAction(w, r, requestContext, "test-mail")
	case r.URL.Path == "/admin":
		if !allows(r.Method, http.MethodGet, http.MethodHead) {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		data := page("플랫폼 관리", "admin", requestContext, canMutateTenant(requestContext), appData.Demo)
		data.Tenants = appData.Tenants
		data.Admin = appData.Admin
		data.AdminWritable = !h.demo
		switch r.URL.Query().Get("result") {
		case "collection":
			data.AdminResult = "수집 작업을 시작했습니다."
		case "test-mail":
			data.AdminResult = "테스트 메일을 발송했습니다."
		case "tenant-invited":
			data.AdminResult = "테넌트 관리자 초대를 보냈습니다."
		}
		h.render(w, "admin", data)
	default:
		h.renderStatus(w, http.StatusNotFound, "찾을 수 없는 페이지", "주소를 확인하거나 대시보드로 이동해 주세요.")
	}
}

func (h *Handler) handleSaveFilter(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	deadlineDays, err := strconv.Atoi(r.FormValue("deadline_days"))
	if err != nil {
		http.Error(w, "마감 여유일을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	var minimumAmount *int64
	if raw := strings.TrimSpace(r.FormValue("min_amount")); raw != "" {
		amount, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || amount < 0 {
			http.Error(w, "최소 금액을 확인해 주세요.", http.StatusBadRequest)
			return
		}
		minimumAmount = &amount
	}
	command := FilterCommand{
		Name:            strings.TrimSpace(r.FormValue("name")),
		IncludeKeywords: strings.TrimSpace(r.FormValue("include_keywords")),
		IncludeMode:     strings.TrimSpace(r.FormValue("include_mode")),
		ExcludeKeywords: strings.TrimSpace(r.FormValue("exclude_keywords")),
		Category:        r.FormValue("category"),
		Region:          strings.TrimSpace(r.FormValue("region")),
		MinimumAmount:   minimumAmount,
		DeadlineDays:    deadlineDays,
		Agency:          strings.TrimSpace(r.FormValue("agency")),
	}
	if command.IncludeMode == "" {
		command.IncludeMode = "any"
	}
	if command.Name == "" || command.DeadlineDays < 0 ||
		!allowedValue(command.IncludeMode, "any", "all") ||
		!allowedValue(command.Category, "공사", "용역", "물품", "외자") ||
		utf8.RuneCountInString(command.Region) > 128 {
		http.Error(w, "필터 이름과 마감 여유일을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if err := h.actions.SaveFilter(r.Context(), requestContext, command); err != nil {
		http.Error(w, "필터를 저장하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/filters?saved=1", http.StatusSeeOther)
}

func allowedValue(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (h *Handler) handleFilterToggle(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := ToggleFilterCommand{
		FilterID: strings.TrimSpace(r.FormValue("filter")),
		Enabled:  r.FormValue("enabled") == "1",
	}
	if command.FilterID == "" {
		http.Error(w, "필터 번호가 올바르지 않습니다.", http.StatusBadRequest)
		return
	}
	if err := h.actions.ToggleFilter(r.Context(), requestContext, command); err != nil {
		http.Error(w, "필터 상태를 저장하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/filters?saved=1", http.StatusSeeOther)
}

func (h *Handler) handleSaveNotification(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	deliveryDays, err := parseDeliveryDays(r)
	if err != nil {
		http.Error(w, "발송 요일을 하나 이상 선택해 주세요.", http.StatusBadRequest)
		return
	}
	command := NotificationCommand{DeliveryTime: r.FormValue("delivery_time"), DeliveryDays: deliveryDays, Timezone: r.FormValue("timezone")}
	if _, err := time.Parse("15:04", command.DeliveryTime); err != nil {
		http.Error(w, "발송 시각을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if _, err := time.LoadLocation(command.Timezone); err != nil {
		http.Error(w, "시간대를 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if err := h.actions.SaveNotification(r.Context(), requestContext, command); err != nil {
		http.Error(w, "알림 설정을 저장하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/notifications?saved=1", http.StatusSeeOther)
}

func parseDeliveryDays(r *http.Request) ([]int, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	values := r.PostForm["delivery_days"]
	if len(values) == 0 {
		return nil, errors.New("no delivery day")
	}
	days := make([]int, 0, len(values))
	seen := make(map[int]bool, len(values))
	for _, value := range values {
		day, err := strconv.Atoi(value)
		if err != nil || day < 0 || day > 6 {
			return nil, errors.New("invalid delivery day")
		}
		if !seen[day] {
			seen[day] = true
			days = append(days, day)
		}
	}
	return days, nil
}

func (h *Handler) handleAddRecipient(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := RecipientCommand{Name: strings.TrimSpace(r.FormValue("name")), Email: strings.ToLower(strings.TrimSpace(r.FormValue("email")))}
	address, err := mail.ParseAddress(command.Email)
	if command.Name == "" || err != nil || address.Address != command.Email {
		http.Error(w, "수신자 이름과 이메일을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if err := h.actions.AddRecipient(r.Context(), requestContext, command); err != nil {
		http.Error(w, "수신자를 추가하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/notifications?saved=1", http.StatusSeeOther)
}

func (h *Handler) handlePlatformAction(w http.ResponseWriter, r *http.Request, requestContext RequestContext, action string) {
	if !canViewAdmin(requestContext) {
		http.Error(w, "플랫폼 관리자 권한이 필요합니다.", http.StatusForbidden)
		return
	}
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	var err error
	switch action {
	case "collection":
		err = h.actions.RunCollection(r.Context(), requestContext)
	case "test-mail":
		err = h.actions.SendTestMail(r.Context(), requestContext)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "플랫폼 작업을 실행하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin?result="+action, http.StatusSeeOther)
}

func (h *Handler) handleSaveSettings(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := SettingsCommand{
		TenantName:   strings.TrimSpace(r.FormValue("tenant_name")),
		ContactEmail: strings.TrimSpace(r.FormValue("contact_email")),
	}
	address, err := mail.ParseAddress(command.ContactEmail)
	if command.TenantName == "" || err != nil || address.Address != command.ContactEmail {
		http.Error(w, "회사명과 대표 이메일을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if err := h.actions.SaveSettings(r.Context(), requestContext, command); err != nil {
		http.Error(w, "환경 설정을 저장하지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (h *Handler) handleInviteTenant(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !canViewAdmin(requestContext) {
		http.Error(w, "플랫폼 관리자 권한이 필요합니다.", http.StatusForbidden)
		return
	}
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := TenantInviteCommand{
		TenantName: strings.TrimSpace(r.FormValue("tenant_name")), ContactEmail: strings.TrimSpace(r.FormValue("contact_email")),
		AdminName: strings.TrimSpace(r.FormValue("admin_name")), AdminEmail: strings.TrimSpace(r.FormValue("admin_email")),
	}
	var err error
	command.ContactEmail, err = plainEmail(command.ContactEmail)
	if command.TenantName == "" || command.AdminName == "" || err != nil {
		http.Error(w, "회사 정보와 초기 관리자 정보를 확인해 주세요.", http.StatusBadRequest)
		return
	}
	command.AdminEmail, err = plainEmail(command.AdminEmail)
	if err != nil {
		http.Error(w, "회사 정보와 초기 관리자 정보를 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if h.onboarding == nil {
		http.Error(w, "초대 기능을 사용할 수 없습니다.", http.StatusServiceUnavailable)
		return
	}
	if err := h.onboarding.InviteTenant(r.Context(), requestContext, command); err != nil {
		handleInvitationError(w, err)
		return
	}
	http.Redirect(w, r, "/admin?result=tenant-invited", http.StatusSeeOther)
}

func (h *Handler) handleInviteMember(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := MemberInviteCommand{
		Name: strings.TrimSpace(r.FormValue("name")), Email: strings.TrimSpace(r.FormValue("email")), Role: strings.TrimSpace(r.FormValue("role")),
	}
	var err error
	command.Email, err = plainEmail(command.Email)
	if command.Name == "" || err != nil || !allowedValue(command.Role, "member", "tenant_admin") || command.Role == "" {
		http.Error(w, "구성원 이름, 이메일, 역할을 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if h.onboarding == nil {
		http.Error(w, "초대 기능을 사용할 수 없습니다.", http.StatusServiceUnavailable)
		return
	}
	if err := h.onboarding.InviteMember(r.Context(), requestContext, command); err != nil {
		handleInvitationError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?result=member-invited", http.StatusSeeOther)
}

func (h *Handler) handleAcceptInvitation(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	command := AcceptInviteCommand{
		Token: strings.TrimSpace(r.FormValue("token")), DisplayName: strings.TrimSpace(r.FormValue("display_name")), Password: r.FormValue("password"),
	}
	passwordBytes := len([]byte(command.Password))
	if command.Token == "" || command.DisplayName == "" || !utf8.ValidString(command.Password) || passwordBytes < 12 || passwordBytes > 72 || command.Password != r.FormValue("password_confirm") {
		http.Error(w, "이름과 12~72바이트 비밀번호를 확인해 주세요.", http.StatusBadRequest)
		return
	}
	if err := h.onboarding.AcceptInvitation(r.Context(), command); err != nil {
		if errors.Is(err, ErrInvitationUnavailable) {
			http.Error(w, "초대가 만료되었거나 이미 사용되었습니다.", http.StatusGone)
			return
		}
		http.Error(w, "계정을 만들지 못했습니다.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login?accepted=1", http.StatusSeeOther)
}

func (h *Handler) handleInspectInvitation(w http.ResponseWriter, r *http.Request, requestContext RequestContext) {
	if !validCSRF(r, requestContext) {
		http.Error(w, "요청을 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		http.Error(w, "초대 링크를 확인해 주세요.", http.StatusBadRequest)
		return
	}
	invitation, err := h.onboarding.Invitation(r.Context(), token)
	if err != nil {
		if errors.Is(err, ErrInvitationUnavailable) {
			http.Error(w, "초대가 만료되었거나 이미 사용되었습니다.", http.StatusGone)
			return
		}
		http.Error(w, "초대 정보를 확인하지 못했습니다. 잠시 후 다시 시도해 주세요.", http.StatusServiceUnavailable)
		return
	}
	data := page("초대 수락", "", requestContext, false, false)
	data.Invitation = invitation
	data.InviteToken = token
	data.InviteExpires = invitation.ExpiresAt.In(time.FixedZone("Asia/Seoul", 9*60*60)).Format("2006.01.02 15:04")
	h.render(w, "accept-invite", data)
}

func plainEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("plain email required")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) {
		return "", errors.New("plain email required")
	}
	return strings.ToLower(address.Address), nil
}

func handleInvitationError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvitationMailDelivery) {
		http.Error(w, "초대는 저장했지만 메일 발송에 실패했습니다. 같은 이메일로 다시 초대해 주세요.", http.StatusBadGateway)
		return
	}
	http.Error(w, "초대를 만들지 못했습니다.", http.StatusInternalServerError)
}

func validCSRF(r *http.Request, requestContext RequestContext) bool {
	want := requestContext.CSRFToken
	got := r.FormValue("_csrf")
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func demoNotImplemented(w http.ResponseWriter) {
	http.Error(w, "읽기 전용 데모에서는 변경 내용을 저장하지 않습니다.", http.StatusNotImplemented)
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	if strings.HasSuffix(r.URL.Path, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.assets.ServeHTTP(w, withPath(r, strings.TrimPrefix(r.URL.Path, "/assets")))
}

func withPath(r *http.Request, path string) *http.Request {
	clone := r.Clone(r.Context())
	clone.URL.Path = path
	return clone
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "화면을 표시하지 못했습니다.", http.StatusInternalServerError)
	}
}

func (h *Handler) renderStatus(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.templates.ExecuteTemplate(w, "status", struct {
		Status  int
		Title   string
		Message string
	}{status, title, message})
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "지원하지 않는 요청 방식입니다.", http.StatusMethodNotAllowed)
}

func allows(method string, methods ...string) bool {
	for _, allowed := range methods {
		if method == allowed {
			return true
		}
	}
	return false
}

func page(title, active string, requestContext RequestContext, writable, demo bool) pageData {
	return pageData{
		Title:       title,
		Active:      active,
		UserName:    requestContext.UserName,
		TenantName:  requestContext.TenantName,
		CurrentDate: time.Now().Format("2006.01.02"),
		Role:        requestContext.Role,
		CSRFToken:   requestContext.CSRFToken,
		Writable:    writable,
		Demo:        demo,
	}
}

func canMutateTenant(requestContext RequestContext) bool {
	if requestContext.TenantID == "" {
		return false
	}
	return requestContext.Role == "tenant_admin" || requestContext.Role == "platform_admin"
}

func canViewAdmin(requestContext RequestContext) bool {
	return requestContext.Role == "platform_admin"
}

func safeURL(value string) string {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return value
}

func formatNumber(value int) string {
	digits := strconv.Itoa(value)
	start := 0
	if strings.HasPrefix(digits, "-") {
		start = 1
	}
	for i := len(digits) - 3; i > start; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func hasDay(days []int, wanted int) bool {
	for _, day := range days {
		if day == wanted {
			return true
		}
	}
	return false
}

func weekdays() []string {
	return []string{"일", "월", "화", "수", "목", "금", "토"}
}

func state(r *http.Request) string {
	switch r.URL.Query().Get("state") {
	case "empty", "error", "loading":
		return r.URL.Query().Get("state")
	default:
		return "ready"
	}
}

func sampleNotices() []noticeView {
	return []noticeView{
		{
			ID: "2026-sample-001", Title: "샘플: 회계감사 용역", Category: "용역", Agency: "샘플 공공기관",
			Region: "전국", Amount: "120,000,000원", Deadline: "2026.09.08 17:00",
			Reasons: []string{"포함 키워드 ‘회계감사’ 일치", "예정금액 5천만원 이상"},
		},
		{
			ID: "2026-sample-002", Title: "샘플: 정보시스템 운영 지원", Category: "용역", Agency: "샘플 연구원",
			Region: "서울", Amount: "85,000,000원", Deadline: "2026.09.10 16:00",
			Reasons: []string{"포함 키워드 ‘운영 지원’ 일치", "지역 ‘서울’ 일치"},
		},
		{
			ID: "2026-sample-003", Title: "샘플: 사무용 장비 구매", Category: "물품", Agency: "샘플 재단",
			Region: "경기", Amount: "42,000,000원", Deadline: "2026.09.12 15:00",
			Reasons: []string{"업종 ‘물품’ 일치", "기관 ‘재단’ 일치"},
		},
	}
}

func filterNotices(notices []noticeView, query, category, region string) []noticeView {
	query = strings.ToLower(query)
	filtered := make([]noticeView, 0, len(notices))
	for _, notice := range notices {
		searchable := strings.ToLower(notice.Title + " " + notice.Agency)
		if query != "" && !strings.Contains(searchable, query) {
			continue
		}
		if category != "" && notice.Category != category {
			continue
		}
		if !matchesRegionSearch(notice.Region, region) {
			continue
		}
		filtered = append(filtered, notice)
	}
	return filtered
}

func matchesRegionSearch(noticeRegion, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	noticeRegion = strings.ToLower(noticeRegion)
	for _, term := range strings.Split(query, ",") {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(noticeRegion, term) {
			return true
		}
	}
	return false
}

func findNotice(notices []noticeView, id string) (noticeView, bool) {
	if id == "" || strings.Contains(id, "/") {
		return noticeView{}, false
	}
	for _, notice := range notices {
		if notice.ID == id {
			return notice, true
		}
	}
	return noticeView{}, false
}

func firstReason(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return reasons[0]
}

func initial(name string) string {
	for _, r := range name {
		return string(r)
	}
	return ""
}

func sampleFilters() []filterView {
	return []filterView{
		{"1", "회계감사 용역", "포함: 회계감사 · 제외: 상주 · 전국", 7, true},
		{"2", "IT 운영 지원", "포함: 운영 지원 · 서울/경기 · 5천만원 이상", 12, true},
		{"3", "사무 장비", "물품 · 기관명에 재단 포함", 3, false},
	}
}

func sampleRecipients() []recipientView {
	return []recipientView{
		{"김담당", "manager@example.com", "수신"},
		{"이관리", "admin@example.com", "수신"},
	}
}

func sampleMembers() []memberView {
	return []memberView{
		{"김담당", "manager@example.com", "담당자"},
		{"이관리", "admin@example.com", "테넌트 관리자"},
	}
}

func sampleTenants() []tenantView {
	return []tenantView{
		{"샘플 주식회사", 2, "오늘 07:00", "정상"},
		{"테스트 협력사", 1, "발송 전", "점검"},
	}
}

type sampleBackend struct{}

func (sampleBackend) Load(context.Context, RequestContext, PageRequest) (AppData, error) {
	return AppData{
		Dashboard: DashboardView{
			LastCollected: "오늘 06:12",
			Collected:     1284,
			NewNotices:    22,
			Matches:       7,
			ActiveFilters: 2,
			RunTime:       "07:00",
			NextDelivery:  "내일 07:00",
			Healthy:       true,
		},
		Notices:      sampleNotices(),
		Filters:      sampleFilters(),
		Recipients:   sampleRecipients(),
		Members:      sampleMembers(),
		Tenants:      sampleTenants(),
		DeliveryTime: "07:00",
		DeliveryDays: []int{1, 2, 3, 4, 5},
		Timezone:     "Asia/Seoul",
		ContactEmail: "admin@example.com",
		Admin: AdminView{
			Healthy:        true,
			LastCollected:  "오늘 06:12",
			CollectedCount: 1284,
			FailedJobs:     0,
		},
		Demo: true,
	}, nil
}
