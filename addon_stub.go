//go:build !addon

package main

// loadAddon — публичная сборка: расширения не подключены.
func loadAddon(b *Bridge) Addon { return nil }
