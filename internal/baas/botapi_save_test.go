package baas

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n-popescu/grid0plus/internal/db"
)

func TestWipingASplatoon3SaveArchivesItFirstAndOnlyReachesYourOwn(t *testing.T) {
	b := newBotHarness(t)
	dir := t.TempDir()
	const prefix = "tenants/t-test/saveRecords/"
	bot := httptest.NewServer(b.srv.BotHandler(BotAPIOptions{
		Token: testBotToken, SaveRecordPrefix: prefix, SaveBackupDir: dir,
	}))
	t.Cleanup(bot.Close)
	b.bot = bot

	b.registerDiscord(aliceDiscord, "alice")
	b.registerDiscord(bobDiscord, "bob")
	alice, err := b.store.GetUserByDiscordID(t.Context(), aliceDiscord)
	if err != nil {
		t.Fatal(err)
	}
	save := []byte("alice's save")
	if err := b.store.PutUGCDocument(t.Context(), db.UGCDocument{
		Name: prefix + alice.ID, Owner: alice.ID, Fields: save, CreateTime: time.Now(), UpdateTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// Bob cannot name Alice's login.
	if resp, _ := b.call("POST", "/bot/v1/splatoon3/save/wipe", bobDiscord, map[string]string{"username": "alice"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else's login: status %d", resp.StatusCode)
	}
	// Bob has no save.
	if resp, body := b.call("POST", "/bot/v1/splatoon3/save/wipe", bobDiscord, nil); resp.StatusCode != http.StatusNotFound || body["code"] != "no_cloud_save" {
		t.Fatalf("no save: status %d, body %v", resp.StatusCode, body)
	}

	resp, body := b.call("POST", "/bot/v1/splatoon3/save/wipe", aliceDiscord, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wipe: status %d, body %v", resp.StatusCode, body)
	}
	if _, err := b.store.GetUGCDocument(t.Context(), prefix+alice.ID); err == nil {
		t.Fatal("the save record is still there")
	}
	archived, err := os.ReadFile(filepath.Join(dir, body["backup"].(string)))
	if err != nil || string(archived) != string(save) {
		t.Fatalf("archive = %q, %v", archived, err)
	}
}
