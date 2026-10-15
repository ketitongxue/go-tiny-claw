package gateway

import (
	"net/url"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

const (
	maxSeenMessages = 4096
	seenTTL         = 24 * time.Hour
)

// Message IDs are platform/account scoped, not user or chat scoped.
func DedupKey(msg channel.InboundMessage) string {
	return "channel:" + url.QueryEscape(msg.Route.Channel) + ":account:" + url.QueryEscape(msg.Route.AccountID) + ":message:" + url.QueryEscape(msg.ID)
}

type deduplicator struct{ seen map[string]time.Time }

func (d *deduplicator) contains(key string, now time.Time) bool {
	at, exists := d.seen[key]
	return exists && now.Sub(at) < seenTTL
}

// Called only after successful enqueue or immediate control-command handling.
// Gateway serializes check/enqueue/remember, including simultaneous retries.
func (d *deduplicator) remember(key string, now time.Time) {
	if d.seen == nil {
		d.seen = make(map[string]time.Time)
	}
	if len(d.seen) >= maxSeenMessages {
		oldestKey := ""
		var oldest time.Time
		for id, at := range d.seen {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = id, at
			}
		}
		delete(d.seen, oldestKey)
	}
	d.seen[key] = now
}
