// Command inklink serves the player-facing Grid0 companion.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"flag"
	"fmt"
	"github.com/n-popescu/grid0plus/config"
	"github.com/n-popescu/grid0plus/internal/db"
	"github.com/n-popescu/grid0plus/internal/health"
	"github.com/n-popescu/grid0plus/internal/inklink"
	sjwt "github.com/n-popescu/grid0plus/internal/jwt"
	"github.com/n-popescu/grid0plus/internal/service"
	"github.com/n-popescu/grid0plus/internal/toyohr"
	"os"
	"strings"
	"time"
)

func main() {
	path := service.ConfigFlag()
	demo := flag.Bool("demo", false, "Use isolated sample data on localhost; never connects to the Grid0 database")
	flag.Parse()
	cfg := config.Default()
	log := service.Logger(cfg.Log, "inklink")
	ctx, cancel := service.SignalContext()
	defer cancel()
	var store db.Store
	var key *rsa.PrivateKey
	var err error
	if *demo {
		store, err = demoStore(ctx)
		if err != nil {
			service.Fatal(log, "seed demo", err)
		}
		key, err = rsa.GenerateKey(rand.Reader, 2048)
		cfg.Inklink.SecureCookies = false
		cfg.Inklink.Listen = "127.0.0.1:8091"
		cfg.NPLN.Rotation = config.Rotation{RegularStages: []string{"19", "26", "2", "10", "6", "14", "9", "12"}, BankaraRules: []string{"1", "2", "3", "4"}, BankaraStages: []string{"1", "2", "3", "4", "5", "6"}, StagesPerSlot: 2}
		log.Info("inklink: isolated demo", "username", "demo", "password", "inklink-demo")
	} else {
		cfg, err = config.Load(*path)
		if err != nil {
			service.Fatal(log, "load config", err)
		}
		pg, e := db.OpenPostgres(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.AutoMigrate)
		if e != nil {
			service.Fatal(log, "open database", e)
		}
		defer pg.Close()
		store = pg
		k, e := sjwt.NewKeyring(cfg.Keys.Dir, cfg.Keys.GenerateIfMissing).Key("inklink")
		err = e
		if e == nil {
			key = k.Private
		}
	}
	if err != nil {
		service.Fatal(log, "load companion key", err)
	}
	if *demo {
		cfg.Inklink.Discord = config.DiscordOAuth{ClientID: os.Getenv("SWITCHNET_INKLINK_DISCORD_CLIENT_ID"), ClientSecret: os.Getenv("SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET"), ClientSecretFile: os.Getenv("SWITCHNET_INKLINK_DISCORD_CLIENT_SECRET_FILE"), RedirectURI: os.Getenv("SWITCHNET_INKLINK_DISCORD_REDIRECT_URI")}
	}
	if cfg.Inklink.Discord.ClientSecretFile != "" && cfg.Inklink.Discord.ClientSecret == "" {
		info, e := os.Stat(cfg.Inklink.Discord.ClientSecretFile)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			service.Fatal(log, "load Discord secret", fmt.Errorf("secret file must exist and be readable only by its owner"))
		}
		raw, e := os.ReadFile(cfg.Inklink.Discord.ClientSecretFile)
		if e != nil {
			service.Fatal(log, "load Discord secret", fmt.Errorf("cannot read secret file"))
		}
		cfg.Inklink.Discord.ClientSecret = strings.TrimSpace(string(raw))
		if cfg.Inklink.Discord.ClientSecret == "" {
			service.Fatal(log, "load Discord secret", fmt.Errorf("secret file is empty"))
		}
	}
	rotation, err := toyohr.RotationFromStrings(cfg.NPLN.Rotation.RegularStages, cfg.NPLN.Rotation.BankaraRules, cfg.NPLN.Rotation.BankaraStages, cfg.NPLN.Rotation.StagesPerSlot)
	if err != nil {
		service.Fatal(log, "parse rotation", err)
	}
	srv, err := inklink.New(inklink.Options{Discord: cfg.Inklink.Discord, Store: store, Key: key, Tenant: cfg.NPLN.Tenant, Rotation: rotation, SecureCookies: cfg.Inklink.SecureCookies, ImageDir: cfg.BAAS.ImageDir, PresenceTTL: time.Duration(cfg.BAAS.PresenceTTL) * time.Second, ConsolePresenceTTL: time.Duration(cfg.BAAS.ConsolePresenceTTL) * time.Second, Log: log, Demo: *demo})
	if err != nil {
		service.Fatal(log, "build companion", err)
	}
	log.Info("inklink: listening", "addr", cfg.Inklink.Listen)
	if err := service.ServeHTTPWithHealth(ctx, log, cfg.Inklink.Listen, srv.Handler(), health.New("inklink").Add("database", store.Ping)); err != nil {
		service.Fatal(log, "serve", err)
	}
}
