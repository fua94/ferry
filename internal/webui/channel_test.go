package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The channel picker is a Slack-style list in the sidebar: every channel on the
// server appears, joined ones open on click and the rest join on first click.
func TestChannelPickerListsEveryChannelInSidebar(t *testing.T) {
	handler := Handler()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("page status = %d", page.Code)
	}
	html := page.Body.String()
	for _, contract := range []string{
		`id="channel-list"`,
		`class="sidebar-channels"`,
		`id="channel-title"`,
		`id="new-channel"`,
		`class="sidebar-item channel-create"`,
	} {
		if !strings.Contains(html, contract) {
			t.Errorf("page does not contain channel contract %q", contract)
		}
	}

	app := httptest.NewRecorder()
	handler.ServeHTTP(app, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if app.Code != http.StatusOK {
		t.Fatalf("app status = %d", app.Code)
	}
	javascript := app.Body.String()
	for _, contract := range []string{
		`localStorage.getItem("ferry_channel")`,
		`localStorage.setItem("ferry_channel", channel)`,
		`async function loadChannels()`,
		`async function switchChannel(channelID)`,
		`async function createChannel()`,
		`async function openChannel(channelID)`,
		`function renderChannelList(channels)`,
		`function channelQuery()`,
		`/api/v1/channels/join`,
		`channel.joined`,
		`newChannelButton.addEventListener("click", createChannel)`,
	} {
		if !strings.Contains(javascript, contract) {
			t.Errorf("app.js does not contain channel contract %q", contract)
		}
	}

	stylesheet := httptest.NewRecorder()
	handler.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/app.css", nil))
	if stylesheet.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", stylesheet.Code)
	}
	for _, contract := range []string{`.channel-list {`, `.channel-item {`, `.channel-title {`} {
		if !strings.Contains(stylesheet.Body.String(), contract) {
			t.Errorf("stylesheet does not contain channel contract %q", contract)
		}
	}
}
