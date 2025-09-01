package watcher

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"tacnet-odenwakun/src/mikopbx"

	"github.com/bwmarrin/discordgo"
)

// 変更方向の種別（色分けに使用）
type ChangeDirection int

const (
	DirNone  ChangeDirection = iota
	DirUp                    // オンライン化のみ
	DirDown                  // オフライン化のみ
	DirMixed                 // 上り下りの混在
)

type Notifier interface {
	Notify(text string) error
}

type DiscordNotifier struct {
	Session   *discordgo.Session
	ChannelID string
}

func (d *DiscordNotifier) Notify(text string) error {
	if d.Session == nil || d.ChannelID == "" {
		return fmt.Errorf("discord notifier not configured")
	}
	_, err := d.Session.ChannelMessageSend(d.ChannelID, text)
	return err
}

// Embed対応の補助インターフェース
type embedNotifier interface {
	NotifyEmbed(content string, embed *discordgo.MessageEmbed) error
}

func (d *DiscordNotifier) NotifyEmbed(content string, embed *discordgo.MessageEmbed) error {
	if d.Session == nil || d.ChannelID == "" {
		return fmt.Errorf("discord notifier not configured")
	}
	_, err := d.Session.ChannelMessageSendComplex(d.ChannelID, &discordgo.MessageSend{
		Content: content,
		Embeds:  []*discordgo.MessageEmbed{embed},
	})
	return err
}

type Watcher struct {
	Client   *mikopbx.Client
	Notifier Notifier
	Interval time.Duration
	// in-memory state
	lastPeer      map[string]string // id -> state
	lastProv      map[string]string // id -> state
	peerNameCache map[string]string // id -> name
}

func New(client *mikopbx.Client, notifier Notifier, interval time.Duration) *Watcher {
	return &Watcher{
		Client:        client,
		Notifier:      notifier,
		Interval:      interval,
		lastPeer:      map[string]string{},
		lastProv:      map[string]string{},
		peerNameCache: map[string]string{},
	}
}

func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()

	// initial fetch
	w.checkOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkOnce()
		}
	}
}

func (w *Watcher) checkOnce() {
	peers, err := w.Client.GetPeersStatuses()
	if err != nil {
		log.Printf("peers fetch error: %v", err)
	} else {
		w.diffAndNotifyPeers(peers)
	}

	regs, err := w.Client.GetRegistry()
	if err != nil {
		log.Printf("registry fetch error: %v", err)
	} else {
		w.diffAndNotifyProviders(regs)
	}
}

func (w *Watcher) diffAndNotifyPeers(peers mikopbx.PeersStatusesResponse) {
	// Build current map
	cur := map[string]string{}
	for _, p := range peers.Data {
		cur[p.ID] = p.State
	}
	// First snapshot: just store and return (no spam)
	if len(w.lastPeer) == 0 {
		w.lastPeer = cur
		return
	}
	// Compare online/offline transitions only
	var changes []string
	hasUp := false
	hasDown := false
	for id, state := range cur {
		prev, ok := w.lastPeer[id]
		if !ok {
			// Newly seen: notify only if it is ONLINE and previously unseen treated as OFFLINE
			if isPeerOnline(state) {
				label := w.resolvePeerLabel(id)
				changes = append(changes, fmt.Sprintf("端末 %s: オフライン → オンライン", label))
				hasUp = true
			}
			continue
		}
		if isPeerOnline(prev) != isPeerOnline(state) {
			from := "オフライン"
			to := "オンライン"
			if isPeerOnline(prev) && !isPeerOnline(state) {
				from, to = "オンライン", "オフライン"
				hasDown = true
			} else {
				hasUp = true
			}
			label := w.resolvePeerLabel(id)
			changes = append(changes, fmt.Sprintf("端末 %s: %s → %s", label, from, to))
		}
	}
	// disappeared peers: treat as going OFFLINE
	for id, prev := range w.lastPeer {
		if _, ok := cur[id]; !ok {
			if isPeerOnline(prev) {
				label := w.resolvePeerLabel(id)
				changes = append(changes, fmt.Sprintf("端末 %s: オンライン → オフライン", label))
				hasDown = true
			}
		}
	}
	if len(changes) > 0 && w.Notifier != nil {
		sort.Strings(changes)
		content := w.pickContent(hasDown, hasUp)
		desc := "- " + strings.Join(changes, "\n- ")
		dir := func() ChangeDirection {
			switch {
			case hasDown && hasUp:
				return DirMixed
			case hasDown:
				return DirDown
			case hasUp:
				return DirUp
			default:
				return DirNone
			}
		}()
		color := chooseColor(dir)
		if en, ok := w.Notifier.(embedNotifier); ok {
			embed := &discordgo.MessageEmbed{
				Title:       "📞 端末のState変更",
				Description: desc,
				Color:       color,
				Timestamp:   time.Now().Format(time.RFC3339),
			}
			_ = en.NotifyEmbed(content, embed)
		} else {
			_ = w.Notifier.Notify(content + "\n" + desc)
		}
	}
	w.lastPeer = cur
}

func (w *Watcher) diffAndNotifyProviders(regs mikopbx.RegistryResponse) {
	cur := map[string]string{}
	for _, r := range regs.Data {
		cur[r.ID] = r.State
	}
	if len(w.lastProv) == 0 {
		w.lastProv = cur
		return
	}
	var changes []string
	hasUp := false
	hasDown := false
	for id, state := range cur {
		prev, ok := w.lastProv[id]
		if !ok {
			if isProviderOnline(state) {
				changes = append(changes, fmt.Sprintf("プロバイダ %s: オフライン → オンライン", id))
				hasUp = true
			}
			continue
		}
		if isProviderOnline(prev) != isProviderOnline(state) {
			from := "オフライン"
			to := "オンライン"
			if isProviderOnline(prev) && !isProviderOnline(state) {
				from, to = "オンライン", "オフライン"
				hasDown = true
			} else {
				hasUp = true
			}
			changes = append(changes, fmt.Sprintf("プロバイダ %s: %s → %s", id, from, to))
		}
	}
	for id, prev := range w.lastProv {
		if _, ok := cur[id]; !ok {
			if isProviderOnline(prev) {
				changes = append(changes, fmt.Sprintf("プロバイダ %s: オンライン → オフライン", id))
				hasDown = true
			}
		}
	}
	if len(changes) > 0 && w.Notifier != nil {
		sort.Strings(changes)
		content := "あれれ〜なんかあったみたいだよ〜"
		desc := "- " + strings.Join(changes, "\n- ")
		dir := func() ChangeDirection {
			switch {
			case hasDown && hasUp:
				return DirMixed
			case hasDown:
				return DirDown
			case hasUp:
				return DirUp
			default:
				return DirNone
			}
		}()
		color := chooseColor(dir)
		if en, ok := w.Notifier.(embedNotifier); ok {
			embed := &discordgo.MessageEmbed{
				Title:       "🌐 プロバイダのステート変更を検知",
				Description: desc,
				Color:       color,
				Timestamp:   time.Now().Format(time.RFC3339),
			}
			_ = en.NotifyEmbed(content, embed)
		} else {
			_ = w.Notifier.Notify(content + "\n" + desc)
		}
	}
	w.lastProv = cur
}

func isPeerOnline(state string) bool {
	// Docs show peer state e.g., "OK", "UNKNOWN". Treat OK as online; others offline.
	return strings.EqualFold(state, "OK")
}

func isProviderOnline(state string) bool {
	// Provider registry state shown as "OK" or "OFF".
	return strings.EqualFold(state, "OK")
}

func chooseColor(dir ChangeDirection) int {
	switch dir {
	case DirDown:
		return 0xE74C3C // red
	case DirUp:
		return 0x2ECC71 // green
	case DirMixed:
		return 0xF1C40F // yellow
	default:
		return 0x95A5A6 // gray (念のため)
	}
}

// ラベル解決（名前が取れれば「名前(ID)」形式、なければIDのみ）
func (w *Watcher) resolvePeerLabel(id string) string {
	if id == "" {
		return id
	}
	if name, ok := w.peerNameCache[id]; ok {
		if name != "" {
			return fmt.Sprintf("%s(%s)", name, id)
		}
		return id
	}
	name, err := w.Client.GetPeerName(id)
	if err != nil {
		log.Printf("resolvePeerLabel error for %s: %v", id, err)
	}
	w.peerNameCache[id] = name
	if name != "" {
		return fmt.Sprintf("%s(%s)", name, id)
	}
	return id
}

// おまけ本文のバリエーション選択（方向で差し替え）
func (w *Watcher) pickContent(hasDown, hasUp bool) string {
	// DOWNを含む: ネガティブ系
	if hasDown {
		downs := []string{
			"あれれ〜なんかあったみたいだよ〜",
			"ｸﾞｴ…",
			"ありゃ？",
		}
		return downs[rand.Intn(len(downs))]
	}
	// UPのみ: ポジティブ系
	ups := []string{
		"お、なんとかなったみたい！",
		"おかえり〜",
		"復活！",
	}
	return ups[rand.Intn(len(ups))]
}
