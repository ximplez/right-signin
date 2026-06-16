package signin

import (
	"testing"

	"right-signin/internal/browser"
	"right-signin/internal/model"
)

func TestDetectSignPageAnchors(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		wantStatus signPageAnchorStatus
	}{
		{
			name:       "nav link ready to sign",
			html:       `<a href="/plugin.php?id=dc_signin:sign&mod=sign&sign=1&mobile=2&infloat=yes&handlekey=qd" data-href="erling_qd-sign_in.html">签到</a>`,
			wantStatus: signAnchorReadyToSign,
		},
		{
			name:       "nav link already signed",
			html:       `<a href="/erling_qd-sign_in.html">已签到</a>`,
			wantStatus: signAnchorAlreadySigned,
		},
		{
			name:       "disabled button already signed",
			html:       `<button class="erqd-checkin-btn2" disabled>已签到</button>`,
			wantStatus: signAnchorAlreadySigned,
		},
		{
			name:       "checkin button ready",
			html:       `<button id="signin-btn" class="erqd-checkin-btn">立即签到</button>`,
			wantStatus: signAnchorReadyToSign,
		},
		{
			name:       "unknown when unrelated html",
			html:       `<div>普通页面内容</div>`,
			wantStatus: signAnchorUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := detectSignPageAnchors(tt.html)
			if got != tt.wantStatus {
				t.Fatalf("detectSignPageAnchors() = %s, want %s", got, tt.wantStatus)
			}
		})
	}
}

func TestClassifySignElementStates(t *testing.T) {
	tests := []struct {
		name       string
		states     []browser.ElementState
		wantStatus model.Status
		wantOK     bool
	}{
		{
			name: "visible sign nav link is ready",
			states: []browser.ElementState{
				{Selector: "a[href*='erling_qd-sign_in.html']", Exists: true, Visible: true, Text: "签到", Href: "/erling_qd-sign_in.html"},
			},
			wantStatus: model.StatusReadyToSign,
			wantOK:     true,
		},
		{
			name: "disabled sign button means already signed",
			states: []browser.ElementState{
				{Selector: "button.erqd-checkin-btn2", Exists: true, Visible: true, Text: "已签到", Class: "erqd-checkin-btn2", Disabled: true},
			},
			wantStatus: model.StatusAlreadySigned,
			wantOK:     true,
		},
		{
			name: "aria disabled button means already signed",
			states: []browser.ElementState{
				{Selector: "#signin-btn", Exists: true, Visible: true, Text: "签到", ID: "signin-btn", AriaDisabled: "true"},
			},
			wantStatus: model.StatusAlreadySigned,
			wantOK:     true,
		},
		{
			name: "hidden matching element is ignored",
			states: []browser.ElementState{
				{Selector: "#signin-btn", Exists: true, Visible: false, Text: "签到", ID: "signin-btn"},
			},
			wantStatus: model.StatusUnknown,
			wantOK:     false,
		},
		{
			name: "unrelated visible element is ignored",
			states: []browser.ElementState{
				{Selector: ".foo", Exists: true, Visible: true, Text: "签到", Class: "foo"},
			},
			wantStatus: model.StatusUnknown,
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, _, gotOK := classifySignElementStates(tt.states)
			if gotOK != tt.wantOK {
				t.Fatalf("classifySignElementStates() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("classifySignElementStates() status = %s, want %s", gotStatus, tt.wantStatus)
			}
		})
	}
}

func TestScoreSignClickCandidatePrefersButtonOverSignActionAnchor(t *testing.T) {
	currentURL := "https://www.right.com.cn/forum/erling_qd-sign_in.html"
	if _, ok := scoreSignClickCandidate(currentURL, browser.ElementState{
		Selector:     "a[href*='erling_qd-sign_in.html']",
		Exists:       true,
		Visible:      true,
		Interactable: true,
		TagName:      "a",
		Text:         "签到",
		Href:         "/forum/erling_qd-sign_in.html",
	}); ok {
		t.Fatalf("expected plain sign-page navigation anchor to be rejected")
	}

	anchorCandidate, anchorOK := scoreSignClickCandidate(currentURL, browser.ElementState{
		Selector:     "a[href*='plugin.php'][href*='sign']",
		Exists:       true,
		Visible:      true,
		Interactable: true,
		TagName:      "a",
		Text:         "签到",
		Href:         "/plugin.php?id=dc_signin:sign&mod=sign&sign=1&infloat=yes&handlekey=qd",
	})
	buttonCandidate, buttonOK := scoreSignClickCandidate(currentURL, browser.ElementState{
		Selector:     "#signin-btn",
		Exists:       true,
		Visible:      true,
		Interactable: true,
		TagName:      "button",
		ID:           "signin-btn",
		Class:        "erqd-checkin-btn",
		Text:         "立即签到",
	})
	if !anchorOK {
		t.Fatalf("expected sign action anchor to remain as low-priority fallback candidate")
	}
	if !buttonOK {
		t.Fatalf("expected real sign button to be clickable candidate")
	}
	if buttonCandidate.Score <= anchorCandidate.Score {
		t.Fatalf("expected sign button score > nav anchor score, got button=%d anchor=%d", buttonCandidate.Score, anchorCandidate.Score)
	}
}

func TestScoreSignClickCandidateRejectsDisabledOrSignedElements(t *testing.T) {
	currentURL := "https://www.right.com.cn/forum/erling_qd-sign_in.html"
	if _, ok := scoreSignClickCandidate(currentURL, browser.ElementState{
		Selector:     "button.erqd-checkin-btn2",
		Exists:       true,
		Visible:      true,
		Interactable: false,
		TagName:      "button",
		Class:        "erqd-checkin-btn2",
		Text:         "已签到",
		Disabled:     true,
	}); ok {
		t.Fatalf("expected disabled signed button to be rejected")
	}
}

func TestShouldRetrySignClickOnlyWhenNoChange(t *testing.T) {
	svc := &Service{}
	if !svc.shouldRetrySignClick(postClickOutcome{Changed: false, Result: model.Result{Status: model.StatusReadyToSign}}, 1, 2) {
		t.Fatalf("expected retry when state is still ready_to_sign and page had no change")
	}
	if svc.shouldRetrySignClick(postClickOutcome{Changed: true, Result: model.Result{Status: model.StatusReadyToSign}}, 1, 2) {
		t.Fatalf("expected no retry when page already changed")
	}
	if svc.shouldRetrySignClick(postClickOutcome{Changed: false, Result: model.Result{Status: model.StatusRiskControl}}, 1, 2) {
		t.Fatalf("expected no retry on terminal risk_control status")
	}
}
