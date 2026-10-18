package baas

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/splat3save"
	"github.com/n-popescu/grid0plus/proto/common"
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

func TestImportingASplatoon3SaveReplacesTheRecordAfterArchivingIt(t *testing.T) {
	b := newBotHarness(t)
	dir := t.TempDir()
	const prefix = "tenants/t-test/saveRecords/"
	var table splat3save.Table
	for i := range table {
		table[i] = uint32(i) * 7919
	}
	bot := httptest.NewServer(b.srv.BotHandler(BotAPIOptions{
		Token: testBotToken, SaveRecordPrefix: prefix, SaveBackupDir: dir, SaveTable: &table,
	}))
	t.Cleanup(bot.Close)
	b.bot = bot

	b.registerDiscord(aliceDiscord, "alice")
	alice, err := b.store.GetUserByDiscordID(t.Context(), aliceDiscord)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond) // a clock ahead of this one
	if err := b.store.PutUGCDocument(t.Context(), db.UGCDocument{
		Name: prefix + alice.ID, Owner: alice.ID, Fields: []byte("old save"), CreateTime: old, UpdateTime: old,
	}); err != nil {
		t.Fatal(err)
	}

	// MS v4, game 11.3.0, an empty local half, a server half {UserName: "A"}.
	plain := []byte{'M', 'S', 0, 4, 0, 11, 3, 0, 0x80, 0x81, 0xce}
	plain = binary.BigEndian.AppendUint32(plain, 0x4540508d)
	plain = append(plain, 0xa1, 'A')
	file := splat3save.Encrypt(plain, 42, &table)

	if resp, body := b.call("POST", "/bot/v1/splatoon3/save/import", aliceDiscord,
		map[string]any{"save": []byte("not a save at all")}); resp.StatusCode != http.StatusUnprocessableEntity || body["code"] != "not_a_save" {
		t.Fatalf("garbage: status %d, body %v", resp.StatusCode, body)
	}
	resp, body := b.call("POST", "/bot/v1/splatoon3/save/import", aliceDiscord, map[string]any{"save": file})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: status %d, body %v", resp.StatusCode, body)
	}
	archived, err := os.ReadFile(filepath.Join(dir, body["backup"].(string)))
	if err != nil || string(archived) != "old save" {
		t.Fatalf("archive = %q, %v", archived, err)
	}
	doc, err := b.store.GetUGCDocument(t.Context(), prefix+alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rec common.MapValue
	if err := proto.Unmarshal(doc.Fields, &rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.GetFields()["UserName"].GetStringValue(); got != "A" || len(rec.GetFields()) != 1 {
		t.Errorf("record = %v, want exactly UserName A", rec.GetFields())
	}
	if !doc.UpdateTime.After(old) {
		t.Errorf("update_time %v did not move past the old record's %v", doc.UpdateTime, old)
	}
}

// A save with a field nobody has named yet still imports; the field is left
// out and the save is kept, with what was left out, for review.
func TestASaveWithAnUnknownFieldImportsTheRestAndIsKeptForReview(t *testing.T) {
	b := newBotHarness(t)
	root := t.TempDir()
	backups := filepath.Join(root, "splat3-save-backups")
	const prefix = "tenants/t-test/saveRecords/"
	var table splat3save.Table
	for i := range table {
		table[i] = uint32(i) * 7919
	}
	bot := httptest.NewServer(b.srv.BotHandler(BotAPIOptions{
		Token: testBotToken, SaveRecordPrefix: prefix, SaveBackupDir: backups, SaveTable: &table,
	}))
	t.Cleanup(bot.Close)
	b.bot = bot
	b.registerDiscord(aliceDiscord, "alice")
	alice, err := b.store.GetUserByDiscordID(t.Context(), aliceDiscord)
	if err != nil {
		t.Fatal(err)
	}

	// {UserName: "A", <an unknown hash>: 1}
	plain := []byte{'M', 'S', 0, 4, 0, 11, 3, 0, 0x80, 0x82, 0xce}
	plain = binary.BigEndian.AppendUint32(plain, 0x4540508d)
	plain = append(plain, 0xa1, 'A', 0xce)
	plain = binary.BigEndian.AppendUint32(plain, 0x9e3779b9)
	plain = append(plain, 0x01)
	file := splat3save.Encrypt(plain, 42, &table)

	resp, body := b.call("POST", "/bot/v1/splatoon3/save/import", aliceDiscord, map[string]any{"save": file})
	if resp.StatusCode != http.StatusOK || body["setAside"] != 1.0 {
		t.Fatalf("import: status %d, body %v", resp.StatusCode, body)
	}
	doc, err := b.store.GetUGCDocument(t.Context(), prefix+alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rec common.MapValue
	if err := proto.Unmarshal(doc.Fields, &rec); err != nil || len(rec.GetFields()) != 1 {
		t.Fatalf("record = %v, %v; want UserName only", rec.GetFields(), err)
	}
	kept, _ := filepath.Glob(filepath.Join(root, "splat3-save-review", alice.ID, "*", "save.dat"))
	if len(kept) != 1 {
		t.Fatalf("kept saves = %v", kept)
	}
	if got, _ := os.ReadFile(kept[0]); string(got) != string(file) {
		t.Error("the kept save is not the uploaded one")
	}
	list, _ := os.ReadFile(filepath.Join(filepath.Dir(kept[0]), "unknown.txt"))
	if !strings.Contains(string(list), "0x9e3779b9") {
		t.Errorf("unknown.txt = %q", list)
	}
}
