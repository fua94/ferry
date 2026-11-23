package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The channel picker is a Slack-style list in the sidebar. Channels are
// visibility rather than permission, so every channel appears undimmed, a click
// only switches the timeline, and each row carries a delete action that leaves
// `default` alone.
func TestChannelPickerListsEveryChannelAndOffersDelete(t *testing.T) {
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
		// The drawer scaffolding: a visible opener, a close control and the
		// backdrop that dismisses it below 760px.
		`id="sidebar-toggle"`,
		`aria-controls="sidebar"`,
		`id="sidebar-close"`,
		`id="sidebar-backdrop"`,
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
		`function channelDeleteButton(channel)`,
		`async function deleteChannel(channelID)`,
		`async function forgetChannel(channelID)`,
		`newChannelButton.addEventListener("click", createChannel)`,
		`for (const channel of channels) {`,
		`row.append(button, channelDeleteButton(channel))`,
		`button.addEventListener("click", () => openChannel(channel.id))`,
		`async function openChannel(channelID) {`,
		// Deleting: confirmation, the real DELETE call and the protected default.
		`window.confirm(`,
		"Delete #${name}? Its messages and attachments are removed for every device.",
		"`/api/v1/channels/${encodeURIComponent(channelID)}`",
		`method: "DELETE"`,
		`if (channel.id === "default")`,
		`button.disabled = true;`,
		`The default channel cannot be deleted.`,
		`if (response.status === 409)`,
		`if (response.status === 404)`,
		`await forgetChannel(channelID)`,
		// Off-canvas drawer below 760px.
		`function setDrawerOpen(open`,
		`document.body.classList.toggle("drawer-open", next)`,
		`window.matchMedia(DRAWER_QUERY)`,
		`sidebarToggle.addEventListener("click"`,
		`sidebarClose.addEventListener("click"`,
		`sidebarBackdrop.addEventListener("click"`,
	} {
		if !strings.Contains(javascript, contract) {
			t.Errorf("app.js does not contain channel contract %q", contract)
		}
	}
	// The picker no longer filters by membership and never calls join: the
	// endpoint is kept only for older clients.
	for _, forbidden := range []string{
		`/api/v1/channels/join`,
		`.joined`,
		`is-unjoined`,
	} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("app.js still depends on channel membership (%q)", forbidden)
		}
	}

	stylesheet := httptest.NewRecorder()
	handler.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/app.css", nil))
	if stylesheet.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", stylesheet.Code)
	}
	css := stylesheet.Body.String()
	for _, contract := range []string{
		`.channel-list {`,
		`.channel-item {`,
		`.channel-title {`,
		`.channel-row {`,
		`.channel-delete {`,
		`--touch-target: 44px;`,
		// The 257px sidebar becomes a drawer instead of a 56px icon rail.
		`@media (max-width: 760px) {`,
		`.drawer-open .sidebar { transform: translateX(0); }`,
		`.sidebar-toggle:not([hidden]) { display: grid; }`,
		`.sidebar-close { display: grid; }`,
		`@media (prefers-color-scheme: dark) {`,
		`@media (prefers-reduced-motion: reduce) {`,
	} {
		if !strings.Contains(css, contract) {
			t.Errorf("stylesheet does not contain channel contract %q", contract)
		}
	}
	for _, forbidden := range []string{
		`.channel-item.is-unjoined`,
		`.sidebar { width: 56px;`,
	} {
		if strings.Contains(css, forbidden) {
			t.Errorf("stylesheet still contains the membership/rail rule %q", forbidden)
		}
	}
}
