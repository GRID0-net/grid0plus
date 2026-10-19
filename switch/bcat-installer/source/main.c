/*
 * switchnet-bcat-installer
 *
 * Fetches a Splatfest's BCAT delivery cache from GRID0+ and writes it into
 * Splatoon 3's BCAT save data — what JKSV does by hand, automatically.
 *
 * WHY THIS EXISTS
 *
 * This is the fallback for a console that does not use GRID0+'s native BCAT
 * serving (see internal/bcat's README and docs/SPLATFESTS.md): the delivery
 * cache is built by the server and the files have to reach the console some
 * other way. Doing it by hand works and does not survive the second fest.
 *
 * NOT COMPILED OR TESTED. There is no devkitPro toolchain in the environment
 * this was written in — no DEVKITPRO, no aarch64-none-elf-gcc — so this is
 * source that has never been built, let alone run on hardware. Treat every line
 * as a proposal. The Makefile beside it is the one devkitPro's switch_rules
 * expects; `make` in this directory with devkitA64 and libnx installed is the
 * whole build.
 *
 * WHAT IT DOES
 *
 *   1. reads switchnet.cfg from the SD card: base URL and dashboard token
 *   2. GET <base>/api/seed/manifest         -> which files, their sizes, digests
 *   3. mounts Splatoon 3's BCAT save data
 *   4. for each file: GET, verify SHA-1, write
 *   5. commits the save data
 *
 * Every step reports to the screen and stops on the first failure, because the
 * alternative is a half-written cache: the console then holds some of a fest's
 * packs and answers BcatInvalid on the ones it does not, hours later, with
 * nothing to connect the two events.
 *
 * Build: make (devkitA64 + libnx + libcurl and mbedtls from portlibs)
 * Licence: AGPL-3.0, matching the rest of GRID0+.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <switch.h>
#include <curl/curl.h>
#include <mbedtls/sha1.h>

/* Splatoon 3's application id. The BCAT save data is per title, so this is what
 * fsOpen_BcatSaveData is asked for -- getting it wrong mounts a different
 * game's cache, which would succeed and then do nothing visible. */
#define SPLATOON3_TITLE_ID 0x0100C2500FC20000ULL

#define MOUNT_NAME "bcat"
#define CONFIG_PATH "/switch/switchnet-bcat-installer/switchnet.cfg"

/* A seed is a handful of files of a few megabytes. Bounded so a malformed
 * manifest cannot make the console allocate without limit. */
#define MAX_MANIFEST 65536
#define MAX_FILE (64 * 1024 * 1024)
#define MAX_FILES 64

typedef struct {
    char path[128];
    char sha1[41];
    long size;
} SeedFile;

typedef struct {
    char base_url[512];
    char token[256];
} Config;

typedef struct {
    unsigned char *data;
    size_t len;
    size_t cap;
} Buffer;

static void wait_for_exit(void) {
    printf("\nPress + to exit.\n");
    consoleUpdate(NULL);

    PadState pad;
    padConfigureInput(1, HidNpadStyleSet_NpadStandard);
    padInitializeDefault(&pad);

    while (appletMainLoop()) {
        padUpdate(&pad);
        if (padGetButtonsDown(&pad) & HidNpadButton_Plus) break;
        consoleUpdate(NULL);
    }
}

static void fail(const char *what, const char *detail) {
    printf("\n[FAILED] %s\n", what);
    if (detail && *detail) printf("         %s\n", detail);
    printf("\nNothing further was written. The cache on this console is\n");
    printf("unchanged, so the fest either works as it did or does not exist\n");
    printf("yet -- it is not half-installed.\n");
    wait_for_exit();
}

/* ---- configuration ---------------------------------------------------- */

/* read_config parses two keys out of a plain text file.
 *
 * A file on the SD card rather than a compiled-in URL, because the operator's
 * address and token are not ours to know, and rebuilding a homebrew to change a
 * hostname is not a workflow. */
static bool read_config(Config *cfg) {
    memset(cfg, 0, sizeof(*cfg));

    FILE *f = fopen(CONFIG_PATH, "r");
    if (!f) return false;

    char line[768];
    while (fgets(line, sizeof(line), f)) {
        char *eq = strchr(line, '=');
        if (!eq) continue;
        *eq = '\0';
        char *key = line;
        char *value = eq + 1;

        /* Trim the trailing newline, and any carriage return: a config edited
         * on Windows otherwise carries \r into the URL and every request 404s
         * for a reason nothing reports. */
        size_t n = strlen(value);
        while (n && (value[n - 1] == '\n' || value[n - 1] == '\r' || value[n - 1] == ' ')) {
            value[--n] = '\0';
        }

        if (strcmp(key, "url") == 0) {
            snprintf(cfg->base_url, sizeof(cfg->base_url), "%s", value);
        } else if (strcmp(key, "token") == 0) {
            snprintf(cfg->token, sizeof(cfg->token), "%s", value);
        }
    }
    fclose(f);
    return cfg->base_url[0] != '\0';
}

/* ---- HTTP ------------------------------------------------------------- */

static size_t on_data(void *chunk, size_t size, size_t nmemb, void *userdata) {
    Buffer *buf = (Buffer *)userdata;
    size_t incoming = size * nmemb;

    if (buf->len + incoming > buf->cap) return 0;
    memcpy(buf->data + buf->len, chunk, incoming);
    buf->len += incoming;
    return incoming;
}

/* fetch GETs a URL into a caller-provided buffer.
 *
 * The buffer is caller-provided and its capacity is the limit: a response bigger
 * than the caller expected fails the transfer rather than growing until the
 * console runs out of memory. */
static bool fetch(const Config *cfg, const char *path, Buffer *buf) {
    char url[1024];
    snprintf(url, sizeof(url), "%s%s", cfg->base_url, path);

    CURL *curl = curl_easy_init();
    if (!curl) return false;

    struct curl_slist *headers = NULL;
    if (cfg->token[0]) {
        char auth[320];
        snprintf(auth, sizeof(auth), "Authorization: Bearer %s", cfg->token);
        headers = curl_slist_append(headers, auth);
    }

    buf->len = 0;
    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, on_data);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, buf);
    curl_easy_setopt(curl, CURLOPT_FOLLOWLOCATION, 1L);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, 120L);
    if (headers) curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers);
    /* GRID0+'s certificate is signed by the operator's own CA, which this
     * console has no reason to trust and no way to be told about from here. The
     * connection is to the operator's own server on the operator's own network,
     * so verification buys nothing -- but it is turned off EXPLICITLY, in one
     * place, with this comment, rather than by a flag somebody finds later and
     * cannot explain. Use http:// if you would rather not have TLS at all than
     * have unverified TLS. */
    curl_easy_setopt(curl, CURLOPT_SSL_VERIFYPEER, 0L);
    curl_easy_setopt(curl, CURLOPT_SSL_VERIFYHOST, 0L);

    CURLcode rc = curl_easy_perform(curl);
    long status = 0;
    curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, &status);

    if (headers) curl_slist_free_all(headers);
    curl_easy_cleanup(curl);

    if (rc != CURLE_OK) {
        printf("  %s: %s\n", path, curl_easy_strerror(rc));
        return false;
    }
    if (status != 200) {
        printf("  %s: HTTP %ld\n", path, status);
        return false;
    }
    return true;
}

/* ---- manifest --------------------------------------------------------- */

/* A hand-rolled scan for the three fields per entry, rather than a JSON parser.
 *
 * The manifest is generated by internal/dashboard/seed.go and its shape is
 * fixed, so a parser would be more code with more ways to be wrong. It is
 * strict: a field it cannot find stops the install, because a manifest entry
 * without its digest is an entry that cannot be verified. */
static const char *find_field(const char *from, const char *key, char *out, size_t out_len) {
    char needle[64];
    snprintf(needle, sizeof(needle), "\"%s\"", key);

    const char *at = strstr(from, needle);
    if (!at) return NULL;
    at = strchr(at + strlen(needle), ':');
    if (!at) return NULL;
    at++;
    while (*at == ' ' || *at == '"') at++;

    size_t i = 0;
    while (*at && *at != '"' && *at != ',' && *at != '}' && i + 1 < out_len) {
        out[i++] = *at++;
    }
    out[i] = '\0';
    return at;
}

static int parse_manifest(const char *json, SeedFile *files, int max_files) {
    const char *files_at = strstr(json, "\"files\"");
    if (!files_at) return -1;

    int count = 0;
    const char *cursor = files_at;
    while (count < max_files) {
        const char *entry = strstr(cursor, "\"path\"");
        if (!entry) break;

        char size_text[32];
        if (!find_field(entry, "path", files[count].path, sizeof(files[count].path))) return -1;
        if (!find_field(entry, "size", size_text, sizeof(size_text))) return -1;
        if (!find_field(entry, "sha1", files[count].sha1, sizeof(files[count].sha1))) return -1;

        files[count].size = strtol(size_text, NULL, 10);
        if (files[count].size <= 0 || files[count].size > MAX_FILE) return -1;

        cursor = entry + 6;
        count++;
    }
    return count;
}

/* ---- verification ----------------------------------------------------- */

static bool digest_matches(const unsigned char *data, size_t len, const char *want_hex) {
    unsigned char sum[20];
    if (mbedtls_sha1(data, len, sum) != 0) return false;

    char got[41];
    for (int i = 0; i < 20; i++) snprintf(got + i * 2, 3, "%02x", sum[i]);
    got[40] = '\0';
    return strcasecmp(got, want_hex) == 0;
}

/* ---- save data -------------------------------------------------------- */

static bool write_into_save(const char *rel_path, const unsigned char *data, size_t len) {
    /* The region directory has to exist before the file. mkdir on a path that
     * already exists is not an error worth stopping for -- the second region's
     * files land in a directory the first one made. */
    char dir[256];
    snprintf(dir, sizeof(dir), MOUNT_NAME ":/%s", rel_path);
    char *slash = strrchr(dir, '/');
    if (slash) {
        *slash = '\0';
        mkdir(dir, 0777);
    }

    char full[256];
    snprintf(full, sizeof(full), MOUNT_NAME ":/%s", rel_path);

    FILE *f = fopen(full, "wb");
    if (!f) return false;
    size_t written = fwrite(data, 1, len, f);
    /* fclose can fail on a filesystem that buffers, and a failure there means
     * the bytes did not land -- so it is checked rather than ignored. */
    bool ok = (written == len) && (fclose(f) == 0);
    return ok;
}

/* ---- main ------------------------------------------------------------- */

int main(int argc, char **argv) {
    (void)argc;
    (void)argv;

    consoleInit(NULL);
    printf("switchnet-bcat-installer\n");
    printf("========================\n\n");

    Config cfg;
    if (!read_config(&cfg)) {
        fail("read " CONFIG_PATH,
             "Create it with two lines:  url=https://your-server:8087  token=...");
        consoleExit(NULL);
        return 1;
    }
    printf("server: %s\n\n", cfg.base_url);

    Result rc = socketInitializeDefault();
    if (R_FAILED(rc)) {
        fail("initialise sockets", NULL);
        consoleExit(NULL);
        return 1;
    }
    curl_global_init(CURL_GLOBAL_DEFAULT);

    /* 1. the manifest */
    printf("fetching the manifest... ");
    consoleUpdate(NULL);

    unsigned char manifest_bytes[MAX_MANIFEST + 1];
    Buffer manifest = {manifest_bytes, 0, MAX_MANIFEST};
    if (!fetch(&cfg, "/api/seed/manifest", &manifest)) {
        fail("fetch the manifest",
             "Check the url and token, and that bcat.seed_dir is set on the server.");
        goto cleanup;
    }
    manifest_bytes[manifest.len] = '\0';
    printf("ok\n");

    static SeedFile files[MAX_FILES];
    int count = parse_manifest((const char *)manifest_bytes, files, MAX_FILES);
    if (count <= 0) {
        fail("read the manifest", "It listed no files, or an entry was missing a field.");
        goto cleanup;
    }

    char fest_id[32] = {0};
    find_field((const char *)manifest_bytes, "festId", fest_id, sizeof(fest_id));
    printf("fest:   %s\n", fest_id[0] ? fest_id : "(unnamed)");
    printf("files:  %d\n\n", count);
    consoleUpdate(NULL);

    /* 2. the save data. Mounted BEFORE anything is downloaded, so a console
     *    that cannot mount it does not spend two minutes fetching first. */
    FsFileSystem bcat_fs;
    rc = fsOpen_BcatSaveData(&bcat_fs, SPLATOON3_TITLE_ID);
    if (R_FAILED(rc)) {
        fail("open Splatoon 3's BCAT save data",
             "Has the game been launched at least once on this console?");
        goto cleanup;
    }
    if (fsdevMountDevice(MOUNT_NAME, bcat_fs) < 0) {
        fsFsClose(&bcat_fs);
        fail("mount the BCAT save data", NULL);
        goto cleanup;
    }

    /* 3. one file at a time, verified before it is written */
    static unsigned char file_bytes[MAX_FILE];
    bool ok = true;

    for (int i = 0; i < count && ok; i++) {
        printf("[%2d/%2d] %s ", i + 1, count, files[i].path);
        consoleUpdate(NULL);

        char path[256];
        snprintf(path, sizeof(path), "/api/seed/file/%s", files[i].path);

        Buffer body = {file_bytes, 0, MAX_FILE};
        if (!fetch(&cfg, path, &body)) {
            ok = false;
            break;
        }
        if ((long)body.len != files[i].size) {
            printf("\n  got %lu bytes, the manifest says %ld\n",
                   (unsigned long)body.len, files[i].size);
            ok = false;
            break;
        }
        /* Verified before writing, not after. A truncated or corrupted pack is
         * indistinguishable from a valid one to the filesystem, and the failure
         * would surface hours later as BcatInvalid in a lobby. */
        if (!digest_matches(body.data, body.len, files[i].sha1)) {
            printf("\n  the digest does not match; refusing to write it\n");
            ok = false;
            break;
        }
        if (!write_into_save(files[i].path, body.data, body.len)) {
            printf("\n  could not write it into the save data\n");
            ok = false;
            break;
        }
        printf("ok\n");
        consoleUpdate(NULL);
    }

    /* 4. commit. Without this the writes are discarded when the device is
     *    unmounted, silently -- the install appears to succeed and the console
     *    holds nothing. */
    if (ok) {
        rc = fsdevCommitDevice(MOUNT_NAME);
        if (R_FAILED(rc)) {
            printf("\ncould not commit the save data\n");
            ok = false;
        }
    }

    fsdevUnmountDevice(MOUNT_NAME);

    if (ok) {
        printf("\nDone. %d files written and committed.\n", count);
        printf("\nStart Splatoon 3. If the fest does not appear, the announced\n");
        printf("id, resource and revision on the server must match the\n");
        printf("summary.yml just written -- `switchnetctl fest build` makes them\n");
        printf("match by construction.\n");
        wait_for_exit();
    } else {
        fail("install the cache", "Some files may not have been written; re-run once fixed.");
    }

cleanup:
    curl_global_cleanup();
    socketExit();
    consoleExit(NULL);
    return ok ? 0 : 1;
}
