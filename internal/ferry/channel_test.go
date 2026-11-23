package ferry

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
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

	channels, err := store.ListChannels(t.Context(), "0123456789abcdef0123456789abcdef")
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
	// the default membership.
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

func TestChannelsIsolateMessagesAndMembership(t *testing.T) {
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
	for _, channelID := range []string{"alpha", "beta"} {
		if err := store.JoinChannel(ctx, alice.ID, channelID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.JoinChannel(ctx, bob.ID, "alpha"); err != nil {
		t.Fatal(err)
	}
	if member, err := store.IsChannelMember(ctx, bob.ID, "beta"); err != nil || member {
		t.Fatalf("bob beta membership = %v, %v", member, err)
	}

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

	alphaMessages, _, err := store.ListMessages(ctx, "alpha", 0, 100)
	if err != nil || len(alphaMessages) != 3 {
		t.Fatalf("alpha messages = %#v, error = %v", alphaMessages, err)
	}
	betaMessages, _, err := store.ListMessages(ctx, "beta", 0, 100)
	if err != nil || len(betaMessages) != 0 {
		t.Fatalf("beta messages = %#v, error = %v", betaMessages, err)
	}
	if _, err := store.CreateTextForDevice(ctx, bob, "beta", "must not land"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-member text error = %v, want ErrUnauthorized", err)
	}
	if _, err := store.CreateFileForDevice(ctx, bob, "beta", "x.txt", "text/plain", strings.NewReader("x")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-member file error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.OpenFile(ctx, "beta", alphaFile.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-channel OpenFile error = %v, want ErrNotFound", err)
	}
	if err := store.RetractMessage(ctx, alice.ID, "beta", alphaText.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-channel recall error = %v, want ErrNotFound", err)
	}

	betaDevices, err := store.ListDevices(ctx, "beta")
	if err != nil || len(betaDevices) != 1 || betaDevices[0].ID != alice.ID {
		t.Fatalf("beta devices = %#v, error = %v", betaDevices, err)
	}
	defaultDevices, err := store.ListDevices(ctx, DefaultChannelID)
	if err != nil || len(defaultDevices) != 2 {
		t.Fatalf("default devices = %#v, error = %v", defaultDevices, err)
	}

	if err := store.RetractMessage(ctx, bob.ID, "alpha", alphaText.ID); err != nil {
		t.Fatalf("member recall in alpha: %v", err)
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
	if created.ID != "trabajo-2" || created.Name != "trabajo-2" || created.CreatedAt == "" {
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

func TestHTTPChannelsCreateListJoinAndIsolation(t *testing.T) {
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
	if len(channels.Channels) != 1 || channels.Channels[0].ID != DefaultChannelID {
		t.Fatalf("initial channels = %#v", channels.Channels)
	}
	assertJSONKeys(t, list.Body.Bytes(), "channels", []string{"channels"})

	created := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"trabajo"}`, owner.Token)
	assertStatus(t, created, http.StatusCreated)
	var channel Channel
	decodeResponse(t, created, &channel)
	assertJSONKeys(t, created.Body.Bytes(), "created channel", []string{"created_at", "id", "joined", "name"})
	if channel.ID != "trabajo" || channel.Name != "trabajo" {
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

	peer := join("Peer", "trabajo")
	sent := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text?channel=trabajo", `{"text":"hola trabajo"}`, peer.Token)
	if sent.Code != http.StatusCreated {
		t.Fatalf("send to trabajo status = %d, body = %s", sent.Code, sent.Body.String())
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
	if messages := read(owner.Token, "/api/v1/messages"); len(messages) != 0 {
		t.Fatalf("default messages = %#v", messages)
	}

	// The peer joined only "trabajo", so default membership stays out.
	devices := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/devices", "", owner.Token)
	var devicePage struct {
		Devices []Device `json:"devices"`
	}
	decodeResponse(t, devices, &devicePage)
	for _, device := range devicePage.Devices {
		if device.ID == peer.Device.ID {
			t.Fatalf("peer joined only trabajo but appears in default devices: %#v", devicePage.Devices)
		}
	}
	// Creating a channel joins its creator; the peer joined the same channel at
	// join time, while nobody was added to default.
	trabajoDevices := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/devices?channel=trabajo", "", owner.Token)
	assertStatus(t, trabajoDevices, http.StatusOK)
	decodeResponse(t, trabajoDevices, &devicePage)
	if len(devicePage.Devices) != 2 {
		t.Fatalf("trabajo devices = %#v", devicePage.Devices)
	}
	seen := map[string]bool{}
	for _, device := range devicePage.Devices {
		seen[device.ID] = true
	}
	if !seen[owner.Device.ID] || !seen[peer.Device.ID] {
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

func TestHTTPDeviceJoinsExistingChannel(t *testing.T) {
	handler, _ := newRawTestApp(t, false)
	owner := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Owner","password":""}`))
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels", `{"name":"trabajo"}`, owner.Token); response.Code != http.StatusCreated {
		t.Fatalf("create channel status = %d, body = %s", response.Code, response.Body.String())
	}
	peer := decodeClaim(t, requestJSON(t, handler, http.MethodPost, "/api/v1/access/join", `{"device_name":"Peer","password":""}`))

	// The picker shows every channel on the server, with joined=false for the
	// ones this device still has to join.
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
		if channel.Joined != (channel.ID == DefaultChannelID) {
			t.Fatalf("peer channel %#v has the wrong joined flag", channel)
		}
	}

	// Before joining, the channel does not exist as far as this device knows.
	if response := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/messages?channel=trabajo", "", peer.Token); response.Code != http.StatusNotFound {
		t.Fatalf("peer before join status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels/join", `{"channel":"trabajo"}`, peer.Token); response.Code != http.StatusNoContent {
		t.Fatalf("join status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodGet, "/api/v1/messages?channel=trabajo", "", peer.Token); response.Code != http.StatusOK {
		t.Fatalf("peer after join status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/messages/text?channel=trabajo", `{"text":"desde el peer"}`, peer.Token); response.Code != http.StatusCreated {
		t.Fatalf("peer send after join status = %d, body = %s", response.Code, response.Body.String())
	}
	// Joining twice is harmless: the same device, the same channel.
	if response := authenticatedRequest(t, handler, http.MethodPost, "/api/v1/channels/join", `{"channel":"trabajo"}`, peer.Token); response.Code != http.StatusNoContent {
		t.Fatalf("second join status = %d, body = %s", response.Code, response.Body.String())
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
