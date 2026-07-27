//go:build !addon

package main

import "context"

// loadAddon — публичная сборка: расширения не подключены.
func loadAddon(b *Bridge) Addon { return nil }

func (b *Bridge) ingestDiscussionComment(context.Context, int64, int, *UserInfo, string, int, int) {
}

func (b *Bridge) redeemLinkCode(context.Context, string, int64) bool { return false }

func (b *Bridge) issueLinkCode(context.Context, int64) (string, int, bool) { return "", 0, false }

func (b *Bridge) issueCabinetLink(context.Context, string, int64, string) (string, int, bool) {
	return "", 0, false
}

func (b *Bridge) autoLinkAccounts(context.Context, int64, int64) {}
