package ferry

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDatabaseGainsDefaultChannelAndMemberships(t *testing.T) {
	dataDir := t.TempDir()
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "ferry.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The v1.0.5 schema: no channels, no device_channels, no channel columns.
	_, err = database.Exec(`
        CREATE TABLE devices (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            kind TEXT,
            token_hash BLOB NOT NULL UNIQUE,
            created_at TEXT NOT NULL
        );
        CREATE TABLE messages (
            sequence INTEGER PRIMARY KEY AUTOINCREMENT,
            id TEXT NOT NULL UNIQUE,
            kind TEXT NOT NULL,
            sender_name TEXT NOT NULL,
            sender_kind TEXT,
            sender_device_id TEXT,
            text_body TEXT,
            file_name TEXT,
            media_type TEXT,
            file_size INTEGER,
            blob_name TEXT,
            created_at TEXT NOT NULL
        );
        CREATE TABLE retractions (
            message_id TEXT PRIMARY KEY,
            sequence INTEGER UNIQUE
        );
        CREATE TABLE access_settings (
            singleton INTEGER PRIMARY KEY,
            salt BLOB NOT NULL,
            verifier BLOB NOT NULL,
            iterations INTEGER NOT NULL
        );
        INSERT INTO devices (id, name, kind, token_hash, created_at)
        VALUES ('0123456789abcdef0123456789abcdef', 'Mac Web', 'mac', zeroblob(32), '2026-08-29T00:00:00Z');
        INSERT INTO messages (id, kind, sender_name, sender_kind, sender_device_id, text_body, created_at)
        VALUES ('fedcba9876543210fedcba9876543210', 'text', 'iPhone 17 Pro', 'iphone', NULL, 'legacy', '2026-08-29T00:00:01Z');
        INSERT INTO retractions (message_id, sequence)
        VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 1);
    `)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	channels, err := store.ListChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].ID != DefaultChannelID || channels[0].Name != DefaultChannelID || !channels[0].Joined {
		t.Fatalf("legacy channels = %#v", channels)
	}
	messages, _, err := store.ListMessages(t.Context(), DefaultChannelID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].ID != "fedcba9876543210fedcba9876543210" {
		t.Fatalf("legacy messages = %#v, error = %v", messages, err)
	}
	ids, _, err := store.ListRetractions(t.Context(), DefaultChannelID, 0, 10)
	if err != nil || len(ids) != 1 || ids[0] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("legacy retractions = %#v, error = %v", ids, err)
	}
	var messageChannel, retractionChannel string
	if err := store.db.QueryRow("SELECT channel_id FROM messages LIMIT 1").Scan(&messageChannel); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT channel_id FROM retractions LIMIT 1").Scan(&retractionChannel); err != nil {
		t.Fatal(err)
	}
	if messageChannel != DefaultChannelID || retractionChannel != DefaultChannelID {
		t.Fatalf("legacy rows were not backfilled: message=%q retraction=%q", messageChannel, retractionChannel)
	}

	// Reopening a migrated database must be idempotent and must not duplicate
	// the default membership row that the legacy backfill created.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	var memberships int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM device_channels WHERE channel_id = ?", DefaultChannelID).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Fatalf("default memberships after reopen = %d", memberships)
	}
}

func TestChannelsIsolateMessagesWithoutMembership(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	create := func(name string) Device {
		t.Helper()
		_, hash, err := newDeviceToken()
		if err != nil {
			t.Fatal(err)
		}
		device, err := store.CreateDevice(ctx, name, hash)
		if err != nil {
			t.Fatal(err)
		}
		return device
	}
	alice := create("Alice")
	bob := create("Bob")
	for _, id := range []string{"alpha", "beta"} {
		if _, err := store.CreateChannel(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// No join calls: every device can already write every channel.
	alphaText, err := store.CreateTextForDevice(ctx, alice, "alpha", "in alpha")
	if err != nil {
		t.Fatal(err)
	}
	alphaFile, err := store.CreateFileForDevice(ctx, alice, "alpha", "alpha.txt", "text/plain", strings.NewReader("alpha bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTextForDevice(ctx, bob, "alpha", "bob in alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTextForDevice(ctx, bob, "beta", "bob in beta"); err != nil {
		t.Fatal(err)
	}
	betaFile, err := store.CreateFileForDevice(ctx, bob, "beta", "beta.txt", "text/plain", strings.NewReader("beta bytes"))
	if err != nil {
		t.Fatal(err)
	}

	alphaMessages, _, err := store.ListMessages(ctx, "alpha", 0, 100)
	if err != nil || len(alphaMessages) != 3 {
		t.Fatalf("alpha messages = %#v, error = %v", alphaMessages, err)
	}
	betaMessages, _, err := store.ListMessages(ctx, "beta", 0, 100)
	if err != nil || len(betaMessages) != 2 {
		t.Fatalf("beta messages = %#v, error = %v", betaMessages, err)
	}
	// Content stays isolated by channel even though access does not.
	if _, _, err := store.OpenFile(ctx, "beta", alphaFile.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-channel OpenFile error = %v, want ErrNotFound", err)
	}
	if err := store.RetractMessage(ctx, alice.ID, "beta", alphaText.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-channel recall error = %v, want ErrNotFound", err)
	}

	// The device list is server-wide, not per channel.
	devices, err := store.ListDevices(ctx)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices = %#v, error = %v", devices, err)
	}
	seen := map[string]bool{}
	for _, device := range devices {
		seen[device.ID] = true
	}
	if !seen[alice.ID] || !seen[bob.ID] {
		t.Fatalf("devices = %#v", devices)
	}

	// Any live device can recall any message in any channel.
	if err := store.RetractMessage(ctx, bob.ID, "alpha", alphaText.ID); err != nil {
		t.Fatalf("shared recall in alpha: %v", err)
	}
	alphaRetractions, _, err := store.ListRetractions(ctx, "alpha", 0, 10)
	if err != nil || len(alphaRetractions) != 1 || alphaRetractions[0] != alphaText.ID {
		t.Fatalf("alpha retractions = %#v, error = %v", alphaRetractions, err)
	}
	betaRetractions, _, err := store.ListRetractions(ctx, "beta", 0, 10)
	if err != nil || len(betaRetractions) != 0 {
		t.Fatalf("beta retractions = %#v, error = %v", betaRetractions, err)
	}
	// The file message recalled through the wrong channel is still live.
	if _, _, err := store.OpenFile(ctx, "alpha", alphaFile.ID); err != nil {
		t.Fatalf("alpha file after cross-channel recall attempt: %v", err)
	}
	if _, _, err := store.OpenFile(ctx, "beta", betaFile.ID); err != nil {
		t.Fatalf("beta file after cross-channel recall attempt: %v", err)
	}
}

func TestChannelCreationValidationAndConflict(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, invalid := range []string{"", "Trabajo", "-work", "work space", "a_b", strings.Repeat("a", MaxChannelIDBytes+1)} {
		if _, err := store.CreateChannel(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateChannel(%q) error = %v, want ErrInvalid", invalid, err)
		}
	}
	created, err := store.CreateChannel(ctx, "trabajo-2")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "trabajo-2" || created.Name != "trabajo-2" || created.CreatedAt == "" || !created.Joined {
		t.Fatalf("created channel = %#v", created)
	}
	if _, err := store.CreateChannel(ctx, "trabajo-2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate channel error = %v, want ErrConflict", err)
	}
	exists, err := store.ChannelExists(ctx, "trabajo-2")
	if err != nil || !exists {
		t.Fatalf("ChannelExists = %v, %v", exists, err)
	}
	exists, err = store.ChannelExists(ctx, "missing")
	if err != nil || exists {
		t.Fatalf("missing ChannelExists = %v, %v", exists, err)
	}
}

func TestJoinMissingChannelLeavesNoDevice(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	_, hash, err := newDeviceToken()
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.DeviceCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeviceWithKindInChannel(ctx, "Nobody", DeviceKindBrowser, hash, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("join missing channel error = %v, want ErrNotFound", err)
	}
	after, err := store.DeviceCount(ctx)
	if err != nil || after != before {
		t.Fatalf("device count after failed join = %d, error = %v", after, err)
	}
}

func TestHTTPChannelsCreateListAndSharedVisibility(t *testing.T) {
	handler, store := newRawTestApp(t, false)
	join := func(name, channel string) claimResponse {
		t.Helper()
		body := `{"device_name":"` + name + `","password":""`
		if channel != "" {
			body += `,"channel":"` + channel + `"`
		}
		body += `}`
		response := requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("join %s in %q status = %d, body = %s", name, channel, response.Code, response.Body.String())
		}
		return decodeClaim(t, response)
	}
	owner := join("Owner", "")

	list := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/channels", "", owner.Token)
	assertStatus(t, list, http.StatusOK)
	var channels struct {
		Channels []Channel `json:"channels"`
	}
	decodeResponse(t, list, &channels)
	if len(channels.Channels) != 1 || channels.Channels[0].ID != DefaultChannelID || !channels.Channels[0].Joined {
		t.Fatalf("initial channels = %#v", channels.Channels)
	}
	assertJSONKeys(t, list.Body.Bytes(), "channels", []string{"channels"})

	created := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"trabajo"}`, owner.Token)
	assertStatus(t, created, http.StatusCreated)
	var channel Channel
	decodeResponse(t, created, &channel)
	assertJSONKeys(t, created.Body.Bytes(), "created channel", []string{"created_at", "id", "joined", "name"})
	if channel.ID != "trabajo" || channel.Name != "trabajo" || !channel.Joined {
		t.Fatalf("created channel = %#v", channel)
	}

	duplicate := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"trabajo"}`, owner.Token)
	if duplicate.Code != http.StatusConflict || errorCode(t, duplicate) != "channel_exists" {
		t.Fatalf("duplicate status = %d, body = %s", duplicate.Code, duplicate.Body.String())
	}
	for _, body := range []string{`{"name":"Trabajo"}`, `{"name":""}`, `{"name":"-x"}`, `{"name":"x","extra":true}`, `{"name":"x","name":"y"}`} {
		response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", body, owner.Token)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Fatalf("body %s status = %d, body = %s", body, response.Code, response.Body.String())
		}
	}

	// A device joined only to default already sees every channel and can read
	// and write trabajo without calling join.
	peer := join("Peer", "")
	peerList := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/channels", "", peer.Token)
	assertStatus(t, peerList, http.StatusOK)
	decodeResponse(t, peerList, &channels)
	if len(channels.Channels) != 2 {
		t.Fatalf("peer channels = %#v", channels.Channels)
	}
	for _, listed := range channels.Channels {
		if !listed.Joined {
			t.Fatalf("peer channel %#v is not visible", listed)
		}
	}

	sent := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text?channel=trabajo", `{"text":"hola trabajo"}`, peer.Token)
	if sent.Code != http.StatusCreated {
		t.Fatalf("send to trabajo status = %d, body = %s", sent.Code, sent.Body.String())
	}
	sentDefault := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text", `{"text":"hola default"}`, peer.Token)
	if sentDefault.Code != http.StatusCreated {
		t.Fatalf("send to default status = %d, body = %s", sentDefault.Code, sentDefault.Body.String())
	}

	read := func(token, path string) []Message {
		t.Helper()
		response := authenticatedRequest(t, handler, http.MethodGet, path, "", token)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		var page struct {
			Messages []Message `json:"messages"`
		}
		decodeResponse(t, response, &page)
		return page.Messages
	}
	if messages := read(owner.Token, "/api/v1/messages?channel=trabajo"); len(messages) != 1 || messages[0].Text == nil || *messages[0].Text != "hola trabajo" {
		t.Fatalf("trabajo messages = %#v", messages)
	}
	if messages := read(owner.Token, "/api/v1/messages"); len(messages) != 1 || messages[0].Text == nil || *messages[0].Text != "hola default" {
		t.Fatalf("default messages = %#v", messages)
	}

	// Device lists are server-wide; the channel parameter only has to exist.
	devices := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/devices", "", owner.Token)
	var devicePage struct {
		Devices []Device `json:"devices"`
	}
	decodeResponse(t, devices, &devicePage)
	if len(devicePage.Devices) != 2 {
		t.Fatalf("devices = %#v", devicePage.Devices)
	}
	seen := map[string]bool{}
	for _, device := range devicePage.Devices {
		seen[device.ID] = true
	}
	if !seen[owner.Device.ID] || !seen[peer.Device.ID] {
		t.Fatalf("devices = %#v", devicePage.Devices)
	}
	trabajoDevices := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/devices?channel=trabajo", "", owner.Token)
	assertStatus(t, trabajoDevices, http.StatusOK)
	decodeResponse(t, trabajoDevices, &devicePage)
	if len(devicePage.Devices) != 2 {
		t.Fatalf("trabajo devices = %#v", devicePage.Devices)
	}

	for _, path := range []string{
		"/api/v1/messages?channel=missing",
		"/api/v1/devices?channel=missing",
	} {
		response := authenticatedRequest(t, handler, http.MethodGet, path, "", peer.Token)
		if response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{
		"/api/v1/messages?channel=Bad%20Slug",
		"/api/v1/devices?channel=Bad",
	} {
		response := authenticatedRequest(t, handler, http.MethodGet, path, "", owner.Token)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text?channel=missing", `{"text":"no"}`, owner.Token); response.Code != http.StatusNotFound {
		t.Fatalf("send to missing channel status = %d, body = %s", response.Code, response.Body.String())
	}

	before, err := store.DeviceCount(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Ghost","password":"","channel":"missing"}`); response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("join missing channel status = %d, body = %s", response.Code, response.Body.String())
	}
	after, err := store.DeviceCount(t.Context())
	if err != nil || after != before {
		t.Fatalf("failed join changed device count: %d -> %d, error = %v", before, after, err)
	}
}

func TestHTTPChannelJoinIsIdempotentNoOp(t *testing.T) {
	handler, store := newRawTestApp(t, false)
	owner := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Owner","password":""}`))
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"trabajo"}`, owner.Token); response.Code != http.StatusCreated {
		t.Fatalf("create channel status = %d, body = %s", response.Code, response.Body.String())
	}
	peer := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Peer","password":""}`))

	// The picker shows every channel on the server, all visible to this device.
	list := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/channels", "", peer.Token)
	assertStatus(t, list, http.StatusOK)
	var listed struct {
		Channels []Channel `json:"channels"`
	}
	decodeResponse(t, list, &listed)
	if len(listed.Channels) != 2 {
		t.Fatalf("peer channels = %#v", listed.Channels)
	}
	for _, channel := range listed.Channels {
		if !channel.Joined {
			t.Fatalf("peer channel %#v is not visible", channel)
		}
	}

	// Without any join call the peer can already read and write the channel.
	if response := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/messages?channel=trabajo", "", peer.Token); response.Code != http.StatusOK {
		t.Fatalf("peer before join status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text?channel=trabajo", `{"text":"desde el peer"}`, peer.Token); response.Code != http.StatusCreated {
		t.Fatalf("peer send before join status = %d, body = %s", response.Code, response.Body.String())
	}

	memberships := func() int {
		t.Helper()
		var count int
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM device_channels WHERE device_id = ?", peer.Device.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := memberships()
	for range 2 {
		if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels/join", `{"channel":"trabajo"}`, peer.Token); response.Code != http.StatusNoContent {
			t.Fatalf("join status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	if after := memberships(); after != before {
		t.Fatalf("join wrote membership rows: %d -> %d", before, after)
	}
	var trabajoRows int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM device_channels WHERE device_id = ? AND channel_id = ?", peer.Device.ID, "trabajo").Scan(&trabajoRows); err != nil {
		t.Fatal(err)
	}
	if trabajoRows != 0 {
		t.Fatalf("join created %d membership rows for trabajo", trabajoRows)
	}

	cases := []struct {
		body string
		want int
	}{
		{`{"channel":"missing"}`, http.StatusNotFound},
		{`{"channel":"Bad"}`, http.StatusBadRequest},
		{`{"channel":""}`, http.StatusBadRequest},
		{`{}`, http.StatusBadRequest},
		{`{"channel":"trabajo","extra":true}`, http.StatusBadRequest},
	}
	for _, test := range cases {
		response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels/join", test.body, peer.Token)
		if response.Code != test.want {
			t.Fatalf("join %s status = %d, want %d, body = %s", test.body, response.Code, test.want, response.Body.String())
		}
	}
	if response := requestJSON(t, handler, http.MethodPost, "/api/v1/channels/join", `{"channel":"trabajo"}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous join status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHTTPDeleteChannelCascadesAndProtectsDefault(t *testing.T) {
	handler, store := newRawTestApp(t, false)
	join := func(name, channel string) claimResponse {
		t.Helper()
		body := `{"device_name":"` + name + `","password":""`
		if channel != "" {
			body += `,"channel":"` + channel + `"`
		}
		body += `}`
		response := requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("join %s in %q status = %d, body = %s", name, channel, response.Code, response.Body.String())
		}
		return decodeClaim(t, response)
	}
	owner := join("Owner", "")
	for _, name := range []string{"alpha", "beta"} {
		if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"`+name+`"}`, owner.Token); response.Code != http.StatusCreated {
			t.Fatalf("create %s status = %d, body = %s", name, response.Code, response.Body.String())
		}
	}
	// Give alpha a legacy membership row so the cascade has one to remove.
	join("Alpha Peer", "alpha")

	defaultMessage, err := store.CreateTextForDevice(t.Context(), owner.Device, DefaultChannelID, "keep default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTextForDevice(t.Context(), owner.Device, "alpha", "delete alpha text"); err != nil {
		t.Fatal(err)
	}
	alphaFile, err := store.CreateFileForDevice(t.Context(), owner.Device, "alpha", "alpha.txt", "text/plain", strings.NewReader("alpha bytes"))
	if err != nil {
		t.Fatal(err)
	}
	alphaRecalled, err := store.CreateTextForDevice(t.Context(), owner.Device, "alpha", "recalled before delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetractMessage(t.Context(), owner.Device.ID, "alpha", alphaRecalled.ID); err != nil {
		t.Fatal(err)
	}
	betaText, err := store.CreateTextForDevice(t.Context(), owner.Device, "beta", "keep beta")
	if err != nil {
		t.Fatal(err)
	}
	betaFile, err := store.CreateFileForDevice(t.Context(), owner.Device, "beta", "beta.txt", "text/plain", strings.NewReader("beta bytes"))
	if err != nil {
		t.Fatal(err)
	}

	linkResponse := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/files/"+alphaFile.ID+"/link?channel=alpha", "", owner.Token)
	assertStatus(t, linkResponse, http.StatusCreated)
	var link struct {
		URL string `json:"url"`
	}
	decodeResponse(t, linkResponse, &link)

	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := store.db.QueryRowContext(t.Context(), query, arguments...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := count("SELECT COUNT(*) FROM messages WHERE channel_id = ?", "alpha"); got != 2 {
		t.Fatalf("alpha messages before delete = %d, want 2", got)
	}
	if got := count("SELECT COUNT(*) FROM retractions WHERE channel_id = ?", "alpha"); got != 1 {
		t.Fatalf("alpha retractions before delete = %d, want 1", got)
	}
	if got := count("SELECT COUNT(*) FROM device_channels WHERE channel_id = ?", "alpha"); got == 0 {
		t.Fatalf("alpha memberships before delete = 0, want the legacy row")
	}

	protected := authenticatedRequest(t, handler, http.MethodDelete, "/api/v1/channels/default", "", owner.Token)
	if protected.Code != http.StatusConflict || errorCode(t, protected) != "channel_protected" {
		t.Fatalf("default delete status = %d, body = %s", protected.Code, protected.Body.String())
	}
	if got := count("SELECT COUNT(*) FROM channels WHERE id = ?", DefaultChannelID); got != 1 {
		t.Fatalf("default channel count after protected delete = %d", got)
	}

	deleted := authenticatedRequest(t, handler, http.MethodDelete, "/api/v1/channels/alpha", "", owner.Token)
	assertStatus(t, deleted, http.StatusNoContent)
	if deleted.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("delete response was cacheable")
	}

	list := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/channels", "", owner.Token)
	assertStatus(t, list, http.StatusOK)
	var channels struct {
		Channels []Channel `json:"channels"`
	}
	decodeResponse(t, list, &channels)
	if len(channels.Channels) != 2 {
		t.Fatalf("channels after delete = %#v", channels.Channels)
	}
	for _, channel := range channels.Channels {
		if channel.ID == "alpha" {
			t.Fatalf("deleted channel still listed: %#v", channel)
		}
	}
	if response := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/messages?channel=alpha", "", owner.Token); response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("read deleted channel status = %d, body = %s", response.Code, response.Body.String())
	}

	for _, query := range []string{
		"SELECT COUNT(*) FROM messages WHERE channel_id = ?",
		"SELECT COUNT(*) FROM retractions WHERE channel_id = ?",
		"SELECT COUNT(*) FROM device_channels WHERE channel_id = ?",
		"SELECT COUNT(*) FROM channels WHERE id = ?",
	} {
		if got := count(query, "alpha"); got != 0 {
			t.Fatalf("%q after delete = %d, want 0", query, got)
		}
	}
	if _, err := os.Stat(filepath.Join(store.blobsDir, alphaFile.ID+".blob")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alpha blob still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.blobsDir, betaFile.ID+".blob")); err != nil {
		t.Fatalf("beta blob was removed with alpha: %v", err)
	}
	if response := authenticatedRequest(t, handler, http.MethodGet, link.URL, "", ""); response.Code != http.StatusNotFound {
		t.Fatalf("download link for deleted channel status = %d, body = %s", response.Code, response.Body.String())
	}

	// Other channels and the default timeline stay intact.
	defaultMessages, _, err := store.ListMessages(t.Context(), DefaultChannelID, 0, 10)
	if err != nil || len(defaultMessages) != 1 || defaultMessages[0].ID != defaultMessage.ID {
		t.Fatalf("default messages after delete = %#v, error = %v", defaultMessages, err)
	}
	betaMessages, _, err := store.ListMessages(t.Context(), "beta", 0, 10)
	if err != nil || len(betaMessages) != 2 || betaMessages[0].ID != betaText.ID {
		t.Fatalf("beta messages after delete = %#v, error = %v", betaMessages, err)
	}
	if _, _, err := store.OpenFile(t.Context(), "beta", betaFile.ID); err != nil {
		t.Fatalf("beta file after delete: %v", err)
	}

	if response := authenticatedRequest(t, handler, http.MethodDelete, "/api/v1/channels/alpha", "", owner.Token); response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("second delete status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodDelete, "/api/v1/channels/missing", "", owner.Token); response.Code != http.StatusNotFound || errorCode(t, response) != "not_found" {
		t.Fatalf("missing delete status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodDelete, "/api/v1/channels/Bad", "", owner.Token); response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
		t.Fatalf("invalid delete status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHTTPWithoutChannelKeepsPreChannelBehaviour(t *testing.T) {
	handler, _ := newRawTestApp(t, false)
	owner := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Old Client","password":""}`))
	sent := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text", `{"text":"compatible"}`, owner.Token)
	if sent.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", sent.Code, sent.Body.String())
	}
	peer := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Second Old Client","password":""}`))
	page := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/messages", "", peer.Token)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"text":"compatible"`) {
		t.Fatalf("default timeline status = %d, body = %s", page.Code, page.Body.String())
	}
	devices := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/devices", "", peer.Token)
	var devicePage struct {
		Devices []Device `json:"devices"`
	}
	decodeResponse(t, devices, &devicePage)
	if len(devicePage.Devices) != 2 {
		t.Fatalf("default devices = %#v", devicePage.Devices)
	}
	assertJSONKeys(t, sent.Body.Bytes(), "created text", []string{"created_at", "id", "is_current_device", "kind", "sender_kind", "sender_name", "sequence", "text"})
}
