package main

import "testing"

func TestCrosspostHealthLabel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		paused  bool
		tg, max crosspostAccessState
		want    string
	}{
		{name: "working", tg: crosspostAccessReady, max: crosspostAccessReady, want: "✅ работает"},
		{name: "paused", paused: true, tg: crosspostAccessMissing, max: crosspostAccessMissing, want: "⏸ на паузе"},
		{name: "telegram rights", tg: crosspostAccessMissing, max: crosspostAccessReady, want: "⚠️ Telegram-бот не администратор TG-канала"},
		{name: "max rights", tg: crosspostAccessReady, max: crosspostAccessMissing, want: "⚠️ MAX-бот не администратор MAX-канала"},
		{name: "both rights", tg: crosspostAccessMissing, max: crosspostAccessMissing, want: "⚠️ Telegram-бот не администратор TG-канала; MAX-бот не администратор MAX-канала"},
		{name: "unknown", tg: crosspostAccessReady, max: crosspostAccessUnknown, want: "⚠️ не удалось проверить права ботов"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := crosspostHealthLabel(tc.paused, tc.tg, tc.max); got != tc.want {
				t.Fatalf("label=%q want=%q", got, tc.want)
			}
		})
	}
}
