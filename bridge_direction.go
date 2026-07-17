package main

import "strings"

func parsePairDirectionArg(arg string) (string, bool) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	arg = strings.ReplaceAll(arg, " ", "")
	arg = strings.ReplaceAll(arg, "→", ">")
	arg = strings.ReplaceAll(arg, "↔", "both")
	switch arg {
	case "", "status":
		return "", true
	case "both", "all", "2way", "twoway", "on", "bi", "bidirectional":
		return "both", true
	case "tg>max", "tg-max", "tg2max", "telegram>max", "telegram-max":
		return "tg>max", true
	case "max>tg", "max-tg", "max2tg", "max>telegram", "max-telegram":
		return "max>tg", true
	default:
		return "", false
	}
}

func pairDirectionHelp() string {
	return "Формат: /bridge direction tg>max | max>tg | both"
}
