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
