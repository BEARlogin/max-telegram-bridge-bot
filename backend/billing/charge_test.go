package billing

import (
	"os"
	"strings"
	"testing"
)

func TestRecurringChargeIncludesNotificationURL(t *testing.T) {
	src, err := os.ReadFile("charge.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "NotificationURL: s.cfg.NotifyURL") {
		t.Fatal("recurring InitRequest must include the T-Bank notification URL")
	}
}
