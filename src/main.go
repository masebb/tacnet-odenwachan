package main

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tacnet-odenwakun/src/mikopbx"
	"tacnet-odenwakun/src/sipclient"
	"tacnet-odenwakun/src/watcher"

	"github.com/bwmarrin/discordgo"
)

// Env vars:
// - DISCORD_TOKEN: Bot token
// - DISCORD_CHANNEL_ID: Channel to post notifications
// - MIKOPBX_BASE_URL: e.g. http://172.16.156.223
// - MIKOPBX_LOGIN, MIKOPBX_PASSWORD: optional for auth (omit if localhost and not required)
// - POLL_INTERVAL_SEC: optional, default 30
// Flags:
// - --debug: enable verbose HTTP logging for MikoPBX client
func main() {
	// Parse flags (debug only)
	debug := flag.Bool("debug", false, "enable verbose HTTP logging for MikoPBX client")
	flag.Parse()
	rand.Seed(time.Now().UnixNano())

	// Discord (env)
	token := os.Getenv("DISCORD_TOKEN")
	channelID := os.Getenv("DISCORD_CHANNEL_ID")
	if token == "" || channelID == "" {
		log.Fatal("DISCORD_TOKEN and DISCORD_CHANNEL_ID must be set")
	}
	ds, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("failed to create Discord session: %v", err)
	}
	// メッセージコマンド対応（!denwachan）用に必要なIntentを付与
	ds.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent
	if err := ds.Open(); err != nil {
		log.Fatalf("failed to open Discord: %v", err)
	}
	defer ds.Close()

	// おまけ: !denwachan コマンドにランダムで返答
	ds.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.Bot {
			return
		}
		// ギルド内メッセージのみ対象
		if m.GuildID == "" {
			return
		}
		if strings.HasPrefix(m.Content, "!denwachan") {
			replies := []string{
				"わぁっ！",
				"なんでしょうか？",
				"どうされましたか",
			}
			s.ChannelMessageSend(m.ChannelID, replies[rand.Intn(len(replies))])
		}
	})

	// SIP: 起動時Register、!oki <number> でINVITE発信
	oki, err := sipclient.NewFromEnv()
	if err != nil {
		log.Fatalf("SIP init error: %v", err)
	}
	if err := oki.Start(); err != nil {
		log.Fatalf("SIP start error: %v", err)
	}
	ds.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.Bot {
			return
		}
		if m.GuildID == "" {
			return
		}
		if strings.HasPrefix(m.Content, "!oki ") {
			parts := strings.Fields(m.Content)
			if len(parts) < 2 {
				s.ChannelMessageSend(m.ChannelID, "使い方: !oki <電話番号>")
				return
			}
			number := parts[1]
			if err := oki.Invite(number); err != nil {
				s.ChannelMessageSend(m.ChannelID, "発信エラー: "+err.Error())
			} else {
				s.ChannelMessageSend(m.ChannelID, "OKIコール発信: "+number)
			}
		}
	})

	// MikoPBX (env)
	base := os.Getenv("MIKOPBX_BASE_URL")
	login := os.Getenv("MIKOPBX_LOGIN")
	pass := os.Getenv("MIKOPBX_PASSWORD")
	if base == "" {
		log.Fatal("MIKOPBX_BASE_URL must be set")
	}
	cli, err := mikopbx.NewClient(base, login, pass)
	if err != nil {
		log.Fatal(err)
	}
	if debug != nil && *debug {
		cli.SetDebug(true)
	}
	if err := cli.Authenticate(); err != nil {
		// PBXが落ちている/未起動でもプロセスは落とさない
		log.Printf("[WARN] MikoPBX authenticate failed (will retry on demand): %v", err)
	}

	// Interval (env)
	interval := 30 * time.Second
	if v := os.Getenv("POLL_INTERVAL_SEC"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil {
			interval = d
		}
	}

	// Watcher
	w := watcher.New(cli, &watcher.DiscordNotifier{Session: ds, ChannelID: channelID}, interval)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	log.Println("Watcher running. Press Ctrl+C to exit.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down...")
	oki.Shutdown()
}
